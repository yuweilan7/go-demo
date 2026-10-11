package cachecourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

var ErrOverload = errors.New("源库预算已满，立即拒绝")
var ErrRollback = errors.New("实验要求回滚")

// DTO 是租户用户的展示数据。结构体按值返回仍不能自动复制 Labels 的 map 和 Tags
// 的底层数组；本课通过 Clone 给各 caller 独立可变数据。Schema是缓存契约版本，
// Version是权威源业务版本，二者不同；Found=false 是明确的负缓存事实。
type DTO struct {
	Tenant  string            `json:"tenant"`
	User    string            `json:"user"`
	Schema  int               `json:"schema"`
	Version int               `json:"version"`
	Name    string            `json:"name"`
	Found   bool              `json:"found"`
	Labels  map[string]string `json:"labels,omitempty"`
	Tags    []string          `json:"tags,omitempty"`
}

// Clone 由缓存/SF结果边界调用，输入是读取中的DTO值副本；另分配map和slice。
// 本课map值是string、slice元素是string，因此复制一层足够；若元素是指针还须递归。
func (d DTO) Clone() DTO {
	labels := make(map[string]string, len(d.Labels))
	for k, v := range d.Labels {
		labels[k] = v
	}
	d.Labels = labels
	d.Tags = append([]string(nil), d.Tags...)
	return d
}

type scope struct {
	Tenant, User string
	Schema       int
}
type counts struct{ Queries, Active, Peak, Rejected int }

// source 拥有读取预算与计数，借用db/table不Close。slots容量2是本课合成策略；
// gate只在课堂读完真实SQL之后扣住一次源工作，permit仍占用，峰值是应用回源工作数。
type source struct {
	db    *sql.DB
	table string
	slots chan struct{}
	mu    sync.Mutex
	stat  counts
}

func newSource(db *sql.DB, table string) *source {
	return &source{db: db, table: table, slots: make(chan struct{}, 2)}
}

// snapshot由实验owner读取计数值副本；锁内复制四个字段，防止Active/Peak取自不同时刻。
func (s *source) snapshot() counts { s.mu.Lock(); defer s.mu.Unlock(); return s.stat }

// Read 由cache miss/SF leader/实验调用。非阻塞send是接纳点：成功才占预算、执行SQL；
// 满时立刻返回ErrOverload，没有给每个失败缓存请求另建无限数据库等待goroutine。
// observer只在SQL完成后通知，hold明确阻塞在真实旧DTO已读出之后。
func (s *source) Read(ctx context.Context, id scope, observer *gate, hold *gate) (DTO, error) {
	if err := s.admitRead(); err != nil {
		return DTO{}, err
	}
	// 接纳成功后立即注册唯一归还，普通返回、SQL失败、取消和panic展开都会先扣Active，
	// 最后才使permit可复用。计数结束后即使被暂停，也只会暂时少接纳，不会报告Peak=3。
	defer s.finishRead(nil)
	s.mu.Lock()
	s.stat.Queries++ // 只在真正Read准备发SELECT时计数；辅助接纳实验不伪造SQL次数。
	s.mu.Unlock()
	d := DTO{Tenant: id.Tenant, User: id.User, Schema: id.Schema, Labels: map[string]string{"source": "mysql"}, Tags: []string{"original"}}
	// :=在本函数作用域声明d；结构体值内新建map/slice。本次SQL填写Version/Name的地址，
	// Scan成功后它们是当前读快照，不会因为后续writer提交而自动改变。
	err := s.db.QueryRowContext(ctx, "SELECT version,name FROM `"+s.table+"` WHERE tenant=? AND user_id=?", id.Tenant, id.User).Scan(&d.Version, &d.Name)
	if errors.Is(err, sql.ErrNoRows) {
		// 没有该行是一项业务事实，Found保持false；网络/权限/解码错误不能进入此分支。
		err = nil
	} else if err == nil {
		d.Found = true
	}
	if err != nil {
		return DTO{}, fmt.Errorf("权威SELECT：%w", err)
	}
	if observer != nil {
		observer.open()
	}
	if hold != nil {
		if err := await(ctx, hold.c, "SQL旧快照后的回填门"); err != nil {
			return DTO{}, err
		}
	}
	return d, nil
}

// admitRead由Read调用，辅助断言也直接使用同一接纳边界。成功send先占一个permit，
// 再在mu下计入Active/Peak；这些已计数工作都仍持permit，故Active不能超过slots容量。
// 请求在send后、计数前被暂停时，快照可以暂时少计一项接纳，不会多计已经释放的工作。
func (s *source) admitRead() error {
	select {
	case s.slots <- struct{}{}:
		// 空struct不携带业务数据，只代表一个permit。成功send后本函数拥有归还责任。
	default:
		s.mu.Lock()
		s.stat.Rejected++
		s.mu.Unlock()
		return ErrOverload
	}
	s.mu.Lock()
	s.stat.Active++
	if s.stat.Active > s.stat.Peak {
		s.stat.Peak = s.stat.Active
	}
	s.mu.Unlock()
	return nil
}

// finishRead是Read成功接纳后的唯一permit归还路径，caller必须恰好调用一次。
// beforeCount只给有限辅助断言观察“旧工作尚未扣数”的转换边界，Read总传nil。
// 旧版先释放slots再Active--，新工作可在旧数未扣时Active++造成capacity2/Peak3；
// 本版先锁内Active--，再释放slots，保证新的接纳不会与旧Active重叠。
func (s *source) finishRead(beforeCount func()) {
	if beforeCount != nil {
		beforeCount() // 辅助测试在这里等自有gate；生产读取没有这个额外等待。
	}
	s.mu.Lock()
	s.stat.Active--
	s.mu.Unlock()
	<-s.slots // 只有计数收尾完成才让permit可被下一项send取得。
}

// currentVersion 由版本键实验调用；额外一次真实SQL是成本，不能把版本当免费神谕。
// 未计入source.Read的Queries，因此实验日志单独标明version_query=1。
func (s *source) currentVersion(ctx context.Context, id scope) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, "SELECT version FROM `"+s.table+"` WHERE tenant=? AND user_id=?", id.Tenant, id.User).Scan(&v)
	return v, err
}

// update 由写实验调用，显式Begin→UPDATE→Commit。rollback=true路径绝不调用publish；
// publish是提交后的缓存失效，成功提交和失效成功是两个事实，不能用单一error反推前者。
// 不提供写事务自动重试：Commit通信失败可能已提交，返回保留UNKNOWN语义。
// committed表示已确认成功提交；false+UNKNOWN并不是确认服务端未提交。
func (s *source) update(ctx context.Context, id scope, name string, rollback bool, publish func() error) (committed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		e := tx.Rollback()
		if !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, e)
		}
	}()
	r, err := tx.ExecContext(ctx, "UPDATE `"+s.table+"` SET version=version+1,name=? WHERE tenant=? AND user_id=?", name, id.Tenant, id.User)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return false, errors.Join(err, errors.New("更新必须影响1行"))
	}
	if rollback {
		return false, ErrRollback
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("Commit结果UNKNOWN，不重试：%w", err)
	}
	committed = true // 在调用Redis前保存已知提交事实；Redis错误不能把它抹掉。
	if publish != nil {
		err = publish()
	}
	return committed, err
}
