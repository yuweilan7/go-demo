// Package review 把新订单状态切片、故意有缺陷的候选代码和独立事实断言放在一起。
// 它只 import 前置公开包，订单的事务/outbox/inbox实现仍由M37唯一业务库负责。
package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// task 是本课手动 goroutine 的 owner 句柄。只有工作 goroutine 写 err；
// Join 先接收已关闭 done，再读 err，channel 的同步关系使这个读不需要额外锁。
// consumed只由实验驱动修改：确认独立断言通过以后，预期故障才算已经处理。
type task struct {
	name     string
	done     chan struct{}
	err      error
	consumed bool
}

// startTask 在 go 语句以前构造最终 done。caller必须立刻把返回指针放进owner账本；
// work普通返回、提前error或panic都会走defer，最终done在recover之后才关闭。
func startTask(name string, work func() error) *task {
	t := &task{name: name, done: make(chan struct{})}
	go func() {
		defer close(t.done) // 最早登记、最后执行；没有“结果发送后仍未收尾”的歧义。
		defer func() {
			if p := recover(); p != nil {
				t.err = panicError(name, p)
			}
		}()
		t.err = work()
	}()
	return t
}

// panicError供任务/实验最外层recover调用。error动态值用%w保留errors.Is身份；
// 普通字符串panic没有error链才用%v。recover不会从panic下一行恢复执行。
func panicError(name string, p any) error {
	if cause, ok := p.(error); ok {
		return fmt.Errorf("%s panic: %w", name, cause)
	}
	return fmt.Errorf("%s panic: %v", name, p)
}

// join以独立Background预算等待真正退出，不复用可能已Canceled的业务ctx。
// error与done分开：非nil业务结果仍可能已安全退出；没done则必须保留资源。
func (t *task) join() error {
	if e := b.JoinDone(t.done, t.name, 10*time.Second); e != nil {
		return e
	}
	return t.err
}

// gate是有界实验的屏障，session拥有Release责任；没有sleep猜调度。
// close release只是通知，使用者仍须等task.done/handler idle才算最终收尾。
type gate struct {
	entered, release chan struct{}
	once             sync.Once
}

func newGate() *gate  { return &gate{entered: make(chan struct{}), release: make(chan struct{})} }
func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

// wait只供单个目标调用；close entered证明已到真实等待点，不证明业务完成。
// ctx取消时直接带原cause返回，不能先放未提交Tx门再猜服务端会看见取消。
func (g *gate) wait(ctx context.Context) error {
	close(g.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.release:
		return nil
	}
}

// await由驱动等观察点；observer超时只是本次观察失败，finish仍会收尾所有使用者。
func await(ctx context.Context, done <-chan struct{}, name string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待%s: %w", name, ctx.Err())
	}
}

// session是一个run的唯一清理owner。随机namespace只是选名字，Provision才做不存在
// 预检→attempt账本→副作用。SQL预算固定3；本课没有额外观察池或OS child。
type session struct {
	r             *b.Resources
	out           io.Writer
	root          *gateway
	clients       []*b.KafkaClient
	gates         []*gate
	tasks         []*task
	ctx           context.Context
	cancel        context.CancelFunc
	beforeCleanup func(context.Context) error
}

// withSession由每个实库案例调用，构造后立即登记defer，连Provision失败也不丢资源。
// err是具名结果槽，defer读最终值并Join主失败/清理失败，不把前者覆盖成成功。
func withSession(ctx context.Context, out io.Writer, work func(*session) error) (err error) {
	c, e := b.LoadConfig()
	if e != nil {
		return e
	}
	ns, e := b.NewNamespace()
	if e != nil {
		return e
	}
	r, e := b.OpenResources(ctx, c, ns, filepath.Join(".local", ns, "owned.json"), out)
	if e != nil {
		return e
	}
	r.SQL.SetMaxOpenConns(3)
	r.SQL.SetMaxIdleConns(1)
	fmt.Fprintf(r.Out, "M39_BUDGET sql_max_open=%d no_extra_observer_pool=true\n", r.SQL.Stats().MaxOpenConnections)
	life, cancel := context.WithCancel(ctx)
	s := &session{r: r, out: r.Out, ctx: life, cancel: cancel}
	defer func() {
		if p := recover(); p != nil {
			err = errors.Join(err, panicError("案例", p))
		}
		err = errors.Join(err, s.finish())
	}()
	if e = r.Provision(ctx); e != nil {
		return e
	}
	return work(s)
}

// makeGate取得屏障后立刻登记；返回指针只是给使用者借用，清理权仍在session。
func (s *session) makeGate() *gate { g := newGate(); s.gates = append(s.gates, g); return g }

// launch只由串行实验驱动调用，在返回task之前已加入清理账本；不是fire-and-forget。
func (s *session) launch(name string, work func() error) *task {
	t := startTask(name, work)
	s.tasks = append(s.tasks, t)
	return t
}

// finish先封入站/发取消，然后放自己的门；gateway会独立join真实handler和所有任务。
// 未Done时不删任何数据、不关仍被借用的池；失败原样保留用于安全人工恢复。
func (s *session) finish() (err error) {
	if s.root != nil {
		s.root.stop()
	}
	s.cancel()
	for _, g := range s.gates {
		g.open()
	}
	safe := true
	if s.root != nil {
		err = errors.Join(err, s.root.close())
		safe = s.root.safe
	}
	for _, t := range s.tasks {
		e := t.join()
		if errors.Is(e, b.ErrCleanup) {
			safe = false
			err = errors.Join(err, e)
		} else if !t.consumed {
			err = errors.Join(err, e)
		}
	}
	for _, c := range s.clients {
		e := c.Close()
		err = errors.Join(err, e)
		safe = safe && e == nil
	}
	if safe && s.beforeCleanup != nil {
		checkCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		checkErr := s.beforeCleanup(checkCtx)
		cancel()
		if checkErr != nil {
			// 使用者已Done但独立业务事实失败：保留中间状态供诊断，不删成“已恢复”。
			return errors.Join(err, checkErr, s.r.Close())
		}
	}
	if !safe {
		return errors.Join(err, b.CleanupCause(errors.New("使用者最终Done缺失；精确对象保留")))
	}
	cleanupErr := s.r.CleanupOwned()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cleanupErr = errors.Join(cleanupErr, b.CleanupCause(s.r.VerifyGone(ctx, s.r.Manifest())))
	cancel()
	cleanupErr = errors.Join(cleanupErr, s.r.Close())
	err = errors.Join(err, cleanupErr)
	// 原工作失败与清理失败分开打印：提前退出测试会保留Canceled，而清理仍可能成功。
	fmt.Fprintf(s.out, "SESSION_DONE namespace=%s safe=%t cleanup_ok=%t returned_error_present=%t sql_budget=3\n", s.r.Namespace, safe, cleanupErr == nil, err != nil)
	return err
}

// Verify是独立只读复核入口：只读caller指定的精确账本，不创建/删除对象或改offset。
// fresh client只开max1观察池，并在返回前关闭；名字校验并不授予清理权。
func Verify(ctx context.Context, path string, out io.Writer) (err error) {
	d, e := b.ReadManifest(path)
	if e != nil {
		return e
	}
	c, e := b.LoadConfig()
	if e != nil {
		return e
	}
	r, e := b.OpenResources(ctx, c, d.Namespace, filepath.Join(".local", "verify", d.Namespace+".json"), out)
	if e != nil {
		return e
	}
	r.SQL.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, r.Close()) }()
	return r.VerifyGone(ctx, d)
}
