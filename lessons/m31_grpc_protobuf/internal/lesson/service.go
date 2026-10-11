package lesson

import (
	"context"
	"io"
	"sync"
	"time"

	pb "go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// gatePlan 是单次请求的协调状态：owner 只释放 gate，handler 关闭 started/done。
// 这些 channel 不传业务数据；关闭是可重复观察的状态通知，done 表示 handler 到了退出清理最后屏障。
// close(done) 仍在 defer 内，不能据此宣称 gRPC 整个请求 goroutine 已退出；最后由 GracefulStop 等它完成。
type gatePlan struct {
	gate, started, committed, done chan struct{}
	once                           sync.Once
}

// newGatePlan 由实验创建，不启动 goroutine；四个 channel 起初均未关闭。
func newGatePlan() *gatePlan {
	return &gatePlan{gate: make(chan struct{}), started: make(chan struct{}), committed: make(chan struct{}), done: make(chan struct{})}
}

// release 让慢 handler 继续；sync.Once 让失败清理和正常释放可安全调用同一方法。
func (p *gatePlan) release() { p.once.Do(func() { close(p.gate) }) }

// watchPlan 记录每一次 Send 的进入/返回；容量覆盖 samples 上限，记录本身不会制造背压。
type watchPlan struct {
	attempts, sent chan int32
	done           chan struct{}
}

// newWatchPlan 的 owner 是实验，handler 只写记录并关闭 done；实验不关闭发送记录 channel。
func newWatchPlan() *watchPlan {
	return &watchPlan{attempts: make(chan int32, 64), sent: make(chan int32, 64), done: make(chan struct{})}
}

// inventory 实现生成的 InventoryServer；嵌入 UnimplementedInventoryServer 满足向前增加 RPC 的接口契约。
// 指针接收者让所有 handler 使用同一库存对象；mu 保护 map，RPC 之间不会自动串行。
type inventory struct {
	pb.UnimplementedInventoryServer
	mu      sync.Mutex
	stock   map[string]int32
	writes  map[string]int
	plans   map[string]*gatePlan
	watches map[string]*watchPlan
	log     *journal
}

// newInventory 为每次 Run 创建合成库存；业务状态在内存中，协议实验真实，持久化不在本课验收范围。
func newInventory(log *journal) *inventory {
	return &inventory{stock: map[string]int32{"001-SKU": 20}, writes: make(map[string]int), plans: make(map[string]*gatePlan), watches: make(map[string]*watchPlan), log: log}
}

// installGate 在请求派发前登记屏障，避免 handler 先执行而 owner 再猜它是否进入。
func (s *inventory) installGate(id string) *gatePlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := newGatePlan()
	s.plans[id] = p
	return p
}

// removeGate 只在原 handler.done 之后调用，让下一次同 ID 请求取得新生命周期；它没有删除写入事实。
func (s *inventory) removeGate(id string) { s.mu.Lock(); defer s.mu.Unlock(); delete(s.plans, id) }

// installWatch 与 installGate 同样只登记控制状态；不会替代真实 gRPC Send/Recv。
func (s *inventory) installWatch(id string) *watchPlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := newWatchPlan()
	s.watches[id] = p
	return p
}

// plansFor 取得共享计划的指针；释放锁后计划字段不变，channel 自身负责通知同步。
func (s *inventory) plansFor(id string) (*gatePlan, *watchPlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plans[id], s.watches[id]
}

// waitGate 是真实慢业务的等待点；调用者决定传请求 ctx 还是错误地丢弃它。
func waitGate(ctx context.Context, p *gatePlan) error {
	select {
	case <-p.gate:
		return nil
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

// Reserve 被生成的 _Inventory_Reserve_Handler 最终调用；返回的 reply/error 将由 gRPC 编码成消息/status。
// req 是本 RPC 的解码对象，s 是全服务器共享对象；不能把 req 的作用域误当成库存的并发保护。
func (s *inventory) Reserve(ctx context.Context, req *pb.ReserveRequest) (*pb.ReserveReply, error) {
	id := req.GetRequestId()
	s.log.add(id, "handler.enter")
	if tenant, ok := ctx.Value(tenantKey{}).(string); ok {
		s.log.add(id, "handler.tenant="+tenant)
	}
	p, _ := s.plansFor(id)
	if p != nil {
		// deferred 闭包在所有正常/错误/panic 返回路径执行；done 不等于外层 interceptor 已完成。
		defer func() { s.log.add(id, "handler.done"); close(p.done) }()
		close(p.started)
	}
	if req.GetSku() == "" || req.GetQuantity() <= 0 || id == "" {
		return nil, status.Error(codes.InvalidArgument, "sku/request_id 必填，quantity 必须为正")
	}
	mode := req.GetMode()
	if mode == pb.ProcessingMode_PANIC_BEFORE_COMMIT {
		panic("课程注入：扣库存之前 panic")
	}
	if mode != pb.ProcessingMode_NORMAL && mode != pb.ProcessingMode_PANIC_BEFORE_COMMIT && p == nil {
		return nil, status.Error(codes.InvalidArgument, "慢模式必须先由实验 owner 登记屏障")
	}
	if mode == pb.ProcessingMode_DROP_CONTEXT_BEFORE_COMMIT {
		// 错误点：用 Background 重新起根，丢失请求 deadline、取消和 metadata/归属值。
		// 仅为坏例子加 2 秒保险；它限制泄漏，却不能恢复客户端取消与库存写入之间的关系。
		lost, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.log.add(id, "wait.lost-context")
		if lost.Value(tenantKey{}) == nil {
			s.log.add(id, "lost.ctx.tenant.present=false")
		}
		if err := waitGate(lost, p); err != nil {
			return nil, err
		}
	} else if mode == pb.ProcessingMode_PROPAGATE_CONTEXT_BEFORE_COMMIT {
		// 修复：沿用 handler 收到的 ctx，等 gate 期间能观察客户端取消，尚未进入扣库存阶段。
		s.log.add(id, "wait.request-context")
		if err := waitGate(ctx, p); err != nil {
			s.log.add(id, "cancel.before-commit")
			return nil, err
		}
	}
	// 坏版本也错误地让副作用忽略 ctx；好版本在锁内检查当前取消状态。
	// 检查与真正写入之间仍不能“撤销未来取消”，这不是数据库事务或分布式幂等承诺。
	remaining, err := s.commit(ctx, req, mode != pb.ProcessingMode_DROP_CONTEXT_BEFORE_COMMIT)
	if err != nil {
		return nil, err
	}
	if mode == pb.ProcessingMode_COMMIT_BEFORE_RESPONSE {
		s.log.add(id, "wait.after-commit")
		if err := waitGate(ctx, p); err != nil {
			s.log.add(id, "cancel.after-commit")
			return nil, err
		}
	}
	return &pb.ReserveReply{RequestId: id, Remaining: remaining}, nil
}

// commit 是唯一库存写点，由 Reserve 同步调用；锁使“检查库存→扣减”成为本进程不可交错的临界区。
// request_id 本课只作追踪，没有去重：同一 ID 重试会再次扣减，实验会证明这个危险。
func (s *inventory) commit(ctx context.Context, req *pb.ReserveRequest, honorContext bool) (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if honorContext && ctx.Err() != nil {
		return 0, status.FromContextError(ctx.Err()).Err()
	}
	available, exists := s.stock[req.GetSku()]
	if !exists {
		return 0, status.Error(codes.NotFound, "SKU 不存在")
	}
	if req.GetQuantity() > available {
		conflict := status.New(codes.FailedPrecondition, "库存不足")
		// details 是带类型的业务事实；WithDetails 本身可能失败，不能把构造错误藏成成功 RPC。
		withDetail, err := conflict.WithDetails(&pb.Conflict{Sku: req.GetSku(), Requested: req.GetQuantity(), Available: available})
		if err != nil {
			return 0, status.Error(codes.Internal, "无法构造业务详情")
		}
		return 0, withDetail.Err()
	}
	s.stock[req.GetSku()] = available - req.GetQuantity()
	s.writes[req.GetRequestId()]++
	s.log.add(req.GetRequestId(), "stock.commit")
	if p := s.plans[req.GetRequestId()]; p != nil {
		close(p.committed)
	}
	return s.stock[req.GetSku()], nil
}

// Inspect 是独立真实读 RPC；实验用新 ctx 发起查询，核对超时之后的服务端业务事实。
func (s *inventory) Inspect(_ context.Context, req *pb.InspectRequest) (*pb.Stock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	available, ok := s.stock[req.GetSku()]
	if !ok {
		return nil, status.Error(codes.NotFound, "SKU 不存在")
	}
	return &pb.Stock{Sku: req.GetSku(), Available: available}, nil
}

// writeCount 是实验的独立内部诊断，不是客户端 RPC 结果；用锁读取写入次数来识别盲目重试副作用。
func (s *inventory) writeCount(id string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.writes[id] }

// Watch 由 gRPC 的流 goroutine 调用，双向流的收发共用同一 stream/context。
// 本 handler 不创建业务 goroutine：顺序 Recv→Send 保持消息顺序，阻塞 Send 时靠 ctx 取消退出。
func (s *inventory) Watch(stream grpc.BidiStreamingServer[pb.WatchRequest, pb.Stock]) error {
	var lastPlan *watchPlan
	var runID string
	defer func() {
		if lastPlan != nil {
			s.log.add(runID, "stream.handler.done")
			close(lastPlan.done)
		}
	}()
	for {
		// Recv 等待客户端下一条消息；客户端 CloseSend 后，已发送消息读完才得到 EOF。
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		} // 客户端半关闭发送，服务端正常结束，客户端最终 Recv EOF。
		if err != nil {
			if lastPlan != nil {
				s.log.add(runID, "stream.recv.error")
			}
			return err
		}
		if lastPlan != nil {
			return status.Error(codes.InvalidArgument, "课程流每次仅发送一个 WatchRequest")
		}
		if req.GetSamples() < 1 || req.GetSamples() > 64 || req.GetPaddingBytes() < 0 || req.GetPaddingBytes() > 256*1024 {
			return status.Error(codes.InvalidArgument, "samples 1..64，padding 0..256KiB")
		}
		_, p := s.plansFor(req.GetRunId())
		lastPlan = p
		runID = req.GetRunId()
		if p == nil {
			return status.Error(codes.InvalidArgument, "未登记流观察计划")
		}
		// 每个课程流仅一个请求，避免同一 done channel 被重复关闭；生产流应建立更一般的生命周期对象。
		for i := int32(1); i <= req.GetSamples(); i++ {
			stock, err := s.Inspect(stream.Context(), &pb.InspectRequest{Sku: req.GetSku()})
			if err != nil {
				return err
			}
			stock.Sequence = i
			stock.Padding = make([]byte, req.GetPaddingBytes())
			p.attempts <- i
			s.log.add(req.GetRunId(), "stream.send.enter")
			// Send 只保证消息交给传输；缓冲/HTTP2 窗口用尽时会等待，不证明客户端 Recv 或处理完成。
			if err := stream.Send(stock); err != nil {
				s.log.add(req.GetRunId(), "stream.send.error")
				return err
			}
			p.sent <- i
			s.log.add(req.GetRunId(), "stream.send.return")
		}
		s.log.add(req.GetRunId(), "stream.recv.wait")
	}
}
