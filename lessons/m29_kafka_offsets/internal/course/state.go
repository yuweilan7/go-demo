package course

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// errCleanup 是资源错误类别，供故障实验辨认“预期业务失败中混入了意外收尾失败”。
// errors.Is沿多重error链查这个身份；不能只见到produce error非nil就算故障验收通过。
var errCleanup = errors.New("资源清理失败")

// Trace 由 Run 创建并共享指针。mu 同时保护事件序号、slice 和 writer，
// 因而日志的 seq 是本进程观察顺序；它不是 Kafka 跨分区的全局 offset。
type Trace struct {
	mu    sync.Mutex
	seq   int
	lines []string
	out   io.Writer
}

// Say 由入口、消费者与回调调用，格式参数只能包含本课合成数据/无秘密状态。
// Mutex 的 Lock 是短暂等待点；unlock 的 defer 在本次写入返回前执行。
func (t *Trace) Say(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	line := fmt.Sprintf(format, args...)
	t.lines = append(t.lines, line)
	fmt.Fprintf(t.out, "[%03d] %s\n", t.seq, line)
}

// Event 是 Kafka value 的 JSON 业务契约。event_id 与生产/消费重试无关而保持稳定；
// offset 是 broker 给每个 partition 的进度，不能代替跨系统的业务唯一键。
type Event struct {
	ID    string `json:"event_id"`
	Order string `json:"order_id"`
	Step  int    `json:"step"`
}

// Effect 是实际写入本机 JSONL 的一条业务副作用；每次 Apply 都有 attempt，
// Applied=false 表示幂等重复没有再次执行效果。文件用于观察，不冒充外部 SQL 事务。
type Effect struct {
	Scope, Owner, EventID, Topic string
	Partition                    int32
	Offset                       int64
	Applied                      bool
}

// Ledger 的 file 与 entries 由 Run 独占生命周期；消费 goroutine 只借用 *Ledger。
// mu 将“检查取消→判断幂等→Write/Sync→更新内存”串行化；scope 隔离不同故障实验。
type Ledger struct {
	mu      sync.Mutex
	file    *os.File
	entries []Effect
	seen    map[string]bool
	trace   *Trace
}

// OpenLedger 由 Run 在启动 consumer 前调用。O_EXCL 防止同名旧报告被覆盖；
// 返回的指针包含共享 mutex/file，不能把整个 Ledger 按值复制后并发使用。
func OpenLedger(path string, trace *Trace) (*Ledger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("建立本 run 独占业务文件: %w", err)
	}
	return &Ledger{file: f, seen: make(map[string]bool), trace: trace}, nil
}

// Apply 由实验 worker 调用。这里的真实副作用是 file.Write + file.Sync，
// 返回 nil 只证明本机文件操作成功，随后仍要单独提交 Kafka offset。
// record/value 在 Kafka 文件解析层已验证；metadata 为值副本，不共享可变 record。
func (l *Ledger) Apply(ctx context.Context, e Effect, dedupe bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// select 同时就绪不能保证取消优先，所以在副作用临界区再显式检查 Err。
	// 一旦通过这行，取消不能回滚已经开始的 Write/Sync；owner 必须等待退出。
	if err := ctx.Err(); err != nil {
		return err
	}
	key := e.Scope + ":" + e.EventID
	e.Applied = !dedupe || !l.seen[key]
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("序列化合成业务副作用: %w", err)
	}
	data = append(data, '\n')
	if _, err := l.file.Write(data); err != nil {
		return fmt.Errorf("写业务 JSONL: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("同步业务 JSONL: %w", err)
	}
	l.entries = append(l.entries, e)
	if e.Applied {
		l.seen[key] = true
	}
	l.trace.Say("BUSINESS scope=%s owner=%s event_id=%s p=%d offset=%d applied=%t", e.Scope, e.Owner, e.EventID, e.Partition, e.Offset, e.Applied)
	return nil
}

// Counts 由断言调用，返回两个 int 值而不泄露内部 slice/共享数组。
// attempts 和 effects 分开，使“重复投递”与“重复业务效果”可以分别验证。
func (l *Ledger) Counts(scope, id, owner string) (attempts, effects int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.Scope == scope && (id == "" || e.EventID == id) && (owner == "" || e.Owner == owner) {
			attempts++
			if e.Applied {
				effects++
			}
		}
	}
	return
}

// Close 只能由 Run 在所有消费者/worker Done 之后调用；否则后台 Apply 会写已关闭文件。
func (l *Ledger) Close() error { return l.file.Close() }

// Watermark 表示某一个 partition 下“下次可安全消费的位置”。owner 独占调用；
// pending 记录已完成却被前面缺口挡住的 offset，绝不能直接取完成集合的最大值。
type Watermark struct {
	Next    int64
	pending map[int64]bool
}

// NewWatermark 的 start 来自此 partition 当前处理批次的第一个 offset。
// map 是新分配的私有对象；每个 partition 必须各有一个 Watermark。
func NewWatermark(start int64) *Watermark {
	return &Watermark{Next: start, pending: make(map[int64]bool)}
}

// Complete 由收集 worker 结果的单一 owner 调用。返回 safeNext 和是否前进；
// 完成 offset=1 而 Next=0 时不会前进，完成 0 后才连续跨过 0、1 到 Next=2。
// 实验 topic 无事务控制记录或日志压实(compaction)缺口；生产跟踪已投递序列，见 README。
func (w *Watermark) Complete(offset int64) (int64, bool) {
	before := w.Next
	if offset >= w.Next {
		w.pending[offset] = true
	}
	for w.pending[w.Next] {
		delete(w.pending, w.Next)
		w.Next++
	}
	return w.Next, w.Next != before
}

// require 把独立业务事实的检查变成可传播 error；panic 留给程序错误而非预期故障。
func require(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf("语义断言失败: "+format, args...)
}

// joinDone 由任务 owner 在 cancel/unblock 之后调用；创建新的 Background 清理预算，
// 不继承已经到期的业务 ctx。done 只能在 goroutine 最后一个 defer 关闭。
// 读取 result 只得到结果，只有这里观察 done 才证明执行与收尾已经结束。
func joinDone(done <-chan struct{}, label string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s 真正 Done: %w", label, ctx.Err())
	}
}

// combineCleanup 供具名 error 返回的 defer 使用，保留业务失败和每项收尾错误。
// errors.Join(nil,nil) 返回 nil；它不会像赋值 err=Close() 那样覆盖原来的失败。
func combineCleanup(dst *error, cleanup error) {
	if cleanup == nil {
		return
	}
	cleanup = fmt.Errorf("%w: %w", errCleanup, cleanup)
	if *dst == nil {
		*dst = cleanup
		return
	}
	*dst = errors.Join(*dst, cleanup)
}
