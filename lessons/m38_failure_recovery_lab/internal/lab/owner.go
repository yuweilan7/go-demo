// Package lab 只编排故障和独立观察，全部业务副作用调用 M37/orderbackend。
package lab

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

var errInjected = errors.New("M38 真实 Tx 内的课程提前返回/panic")

// gate 是 owner 创建的单次阶段门。entered 仅表示业务走到了真实 hook；observed
// 表示 hook 已读到 ctx.Err。二者均不代表 handler 或 caller 的最后一条 defer 完成。
type gate struct {
	entered, observed, release        chan struct{}
	inOnce, observedOnce, releaseOnce sync.Once
}

func newGate() *gate {
	return &gate{entered: make(chan struct{}), observed: make(chan struct{}), release: make(chan struct{})}
}
func (g *gate) open() { g.releaseOnce.Do(func() { close(g.release) }) }

// wait 被同步 hook 借用，不再创建任务。select 可能同时看见取消和放行，所以醒来后
// 统一检查 ctx.Err；observer 可依 observed 确认服务端确实观察取消，再继续收尾。
func (g *gate) wait(ctx context.Context) error {
	g.inOnce.Do(func() { close(g.entered) })
	select {
	case <-ctx.Done():
	case <-g.release:
	}
	if err := ctx.Err(); err != nil {
		g.observedOnce.Do(func() { close(g.observed) })
		return err
	}
	return nil
}

// await 只等待指定通知。调用者必须区分阶段通知和资源 Done；超时交回原错误，
// 不在这里强行把未退出任务标成完成。所有异步资源的最终等待另用 JoinDone。
func await(ctx context.Context, done <-chan struct{}, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", name, ctx.Err())
	}
}

// job 的值槽只由工作 goroutine 写；owner 看到 Done 关闭后才读 err。channel 关闭
// 的同步保证可见性，不需要把 err 加锁。cancel 只是通知，finish 还必须等待 Done。
type job struct {
	done     chan struct{}
	cancel   context.CancelFunc
	err      error
	consumed bool
}

// launch 由各实验调用，返回后立刻登记在 session.jobs。闭包捕获的是原对象指针，
// work 参数 ctx 则是这次工作的接口值副本；它们仍共享同一个取消节点。
func launch(ctx context.Context, work func(context.Context) error) *job {
	life, cancel := context.WithCancel(ctx)
	j := &job{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(j.done) // 最先注册，最后执行：panic 恢复和 cancel 都发生在 Done 之前。
		defer cancel()
		defer func() {
			if p := recover(); p != nil {
				j.err = errors.Join(j.err, panicCause("job", p))
			}
		}()
		j.err = work(life)
	}()
	return j
}

func (j *job) finish() error {
	j.cancel()
	return b.JoinDone(j.done, "本课 caller/worker", 10*time.Second)
}

// session 是一组实验的唯一资源 owner。original 的精确对象账本始终由它保持；
// business 是恢复用的借用者，不 Provision 旧状态、不转移 original 的清理责任。
// original SQL max1 + 一个 business SQL max3 = 总预算4，所有阶段顺序创建 business。
type session struct {
	cfg                b.Config
	original, business *b.Resources
	apps               []*b.App
	clients            []*b.KafkaClient
	gates              []*gate
	jobs               []*job
	processes          []*process
	proxies            []*wireProxy
	httpProxies        []*httpDrop
	dir                string
	out                io.Writer
}

// openSession 的 cfg 只在内存带秘密；dir 是本模块忽略 .local 中的随机 run 目录。
// 取得 original 后立即建立失败 guard，Provision 失败仍按已登记 attempt 精确收尾。
func openSession(ctx context.Context, out io.Writer) (s *session, err error) {
	cfg, err := b.LoadConfig()
	if err != nil {
		return nil, err
	}
	ns, err := b.NewNamespace()
	if err != nil {
		return nil, err
	}
	s = &session{cfg: cfg, dir: filepath.Join(".local", ns), out: out}
	s.original, err = b.OpenResources(ctx, cfg, ns, filepath.Join(s.dir, "original.json"), out)
	if err != nil {
		return nil, err
	}
	s.original.SQL.SetMaxOpenConns(1)
	defer func() {
		if err != nil {
			err = errors.Join(err, s.finish())
			s = nil
		}
	}()
	if err = s.original.Provision(ctx); err != nil {
		return s, err
	}
	return s, nil
}

// authorize 是可信原 owner 的预登记。Invalidate 在不存在预检/账本落盘后写精确键；
// 恢复 child 仅借这些名单。随机名字或合法 JSON 本身均不能构成授权。
func (s *session) authorize(ctx context.Context, keys ...string) error {
	for _, key := range keys {
		if e := s.original.Cache.Invalidate(ctx, "alice", b.OrderID("alice", key)); e != nil {
			return e
		}
	}
	return nil
}

// fresh 先要求旧 business 完全关闭，随后只打开新 client 并授予原精确键。
// cfg 可以仅替换本课代理地址；用户名/口令仍来自 LoadConfig，不复制到配置文件。
func (s *session) fresh(ctx context.Context, cfg b.Config, label string) error {
	if s.business != nil {
		return errors.New("旧 business 仍未 close，禁止第二写者")
	}
	r, e := b.OpenResources(ctx, cfg, s.original.Namespace, filepath.Join(s.dir, label+".json"), s.out)
	if e != nil {
		return e
	}
	s.business = r // 在后续失败前登记；finish 总能看到当前 client。
	r.SQL.SetMaxOpenConns(3)
	return r.BorrowCacheKeys(s.original.Manifest())
}

// closeBusiness 只回收连接，旧事实仍在。App 必须由 caller 先 Close/Done；这里不
// 自动删表或 reset offset，下一 fresh 将继续同一订单、事件与进度。
func (s *session) closeBusiness() error {
	if s.business == nil {
		return nil
	}
	e := s.business.Close()
	if e == nil {
		s.business = nil
	}
	return e
}

// app由实验创建当前应用，Hooks值复制但其门仍由session拥有。成功构造后先登记
// guard再Start，未启动/半启动/已启动三条退出路径都由M37 App.Close承担。
func (s *session) app(h b.Hooks) (*b.App, error) {
	a, e := b.NewApp(s.business, h)
	if e != nil {
		return nil, e
	}
	s.apps = append(s.apps, a) // Start 前登记，构造后提前返回也会 Close 未启动 App。
	if e = a.Start(); e != nil {
		return a, e
	}
	return a, nil
}

// finish 的顺序是协议：停止入站→取消/放门→进程 Kill+Wait/代理关闭/worker join→
// App 最终 Done→借用者自己的账本→原 owner 账本→独立读回→最后关池。
// errors.Join 保留所有原因；清理失败不会吞掉原故障，也不会触发“1062当成功”。
func (s *session) finish() (err error) {
	for _, a := range s.apps {
		a.Stop()
	}
	for _, j := range s.jobs {
		j.cancel()
	}
	for _, g := range s.gates {
		g.open()
	}
	safe := true
	for _, p := range s.processes {
		err = errors.Join(err, p.stop())
		safe = safe && p.safe()
	}
	for _, p := range s.proxies {
		err = errors.Join(err, p.close())
		safe = safe && p.safe()
	}
	for _, p := range s.httpProxies {
		err = errors.Join(err, p.close())
		safe = safe && p.safe()
	}
	for _, j := range s.jobs {
		e := j.finish()
		err = errors.Join(err, e)
		safe = safe && e == nil
		if e == nil && !j.consumed {
			err = errors.Join(err, j.err)
		}
	}
	for i := len(s.apps) - 1; i >= 0; i-- {
		a := s.apps[i]
		e := a.Close()
		err = errors.Join(err, e)
		safe = safe && a.SafeToRelease() && !errors.Is(e, b.ErrCleanup)
	}
	// 手动consumer/standby同样立即登记。驱动Close超时或清理错误时保守保留
	// 数据；外层poll goroutine结束不能替代驱动自己的最终关闭。
	for _, c := range s.clients {
		e := c.Close()
		err = errors.Join(err, e)
		safe = safe && e == nil
	}
	if !safe {
		return errors.Join(err, b.CleanupCause(errors.New("owner 尚有未 Done 使用者；保留对象禁止删状态")))
	}
	if s.business != nil {
		err = errors.Join(err, s.business.CleanupOwned(), s.business.Close())
		s.business = nil
	}
	err = errors.Join(err, s.original.CleanupOwned())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = errors.Join(err, b.CleanupCause(s.original.VerifyGone(ctx, s.original.Manifest())))
	cancel()
	err = errors.Join(err, s.original.Close())
	fmt.Fprintf(s.out, "SESSION_DONE namespace=%s processes=%d proxies=%d jobs=%d safe=%t cleanup_ok=%t\n", s.original.Namespace, len(s.processes), len(s.proxies)+len(s.httpProxies), len(s.jobs), safe, err == nil)
	return err
}

// withSession 让所有实验共用同一最终 guard。panic 先转成可报告错误，再执行 finish；
// 具名 err 是 defer 读取的最终返回槽，不会把工作错误遮蔽成清理成功。
func withSession(ctx context.Context, out io.Writer, work func(*session) error) (err error) {
	s, e := openSession(ctx, out)
	if e != nil {
		return e
	}
	defer func() {
		if p := recover(); p != nil {
			err = errors.Join(err, panicCause("实验", p))
		}
		err = errors.Join(err, s.finish())
	}()
	return work(s)
}

// panicCause 对error动态值使用%w保持errors.Is/As身份；字符串panic才转文本。
func panicCause(name string, p any) error {
	if cause, ok := p.(error); ok {
		return fmt.Errorf("%s panic: %w", name, cause)
	}
	return fmt.Errorf("%s panic: %v", name, p)
}

// Verify 是只读维护入口；manifest 是 caller 指明的精确对象集合，绝不扫描前缀。
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
