package lesson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	pb "go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Report 是实验完成后交给 CLI/独立测试的事实快照；包含服务端写入次数、真实流结果和连接最终状态。
type Report struct {
	FinalStock                                           int32
	ConflictAvailable                                    int32
	LostContextWrites, PropagatedWrites, CommittedWrites int
	StreamReceived                                       int
	BlockedSend                                          int32
	ConnectionReleased                                   bool
}

// Run 是课程唯一正常入口；每次调用有独立库存、listener/Conn、journal 和最多 12 秒的实验范围。
// named err 让资源清理错误能合并返回，defer 不会把已发生的业务事实改成“没发生”。
func Run(parent context.Context, out io.Writer) (report Report, err error) {
	if out == nil {
		out = io.Discard
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	if err = runCompatibility(out); err != nil {
		return report, err
	}
	h, err := startHarness(&journal{out: out})
	if err != nil {
		return report, err
	}
	defer func() {
		closeErr := h.close()
		err = errors.Join(err, closeErr)
		report.ConnectionReleased = closeErr == nil
	}()
	if err = h.waitReady(ctx); err != nil {
		return report, err
	}
	if err = runNormalAndConflict(ctx, h, &report); err != nil {
		return report, err
	}
	if err = runDeadlines(ctx, h, &report); err != nil {
		return report, err
	}
	if err = runStreams(ctx, h, &report); err != nil {
		return report, err
	}
	report.FinalStock, err = h.inspect(ctx)
	if err != nil {
		return report, err
	}
	if err = check(report.FinalStock == 12, "最终库存应为 20-2-2-2-2=12，实际 %d", report.FinalStock); err != nil {
		return report, err
	}
	fmt.Fprintf(out, "PASS stock=%d lost_ctx_writes=%d propagated_writes=%d committed_and_retry_writes=%d stream_messages=%d blocked_send=%d\n", report.FinalStock, report.LostContextWrites, report.PropagatedWrites, report.CommittedWrites, report.StreamReceived, report.BlockedSend)
	return report, nil
}

// runNormalAndConflict 先把一次成功 RPC 从 client 链追到 handler，再验证状态码、typed details、metadata 和 panic 边界。
func runNormalAndConflict(ctx context.Context, h *harness, report *Report) error {
	var header, trailer metadata.MD
	reply, err := h.client.Reserve(ownedContext(ctx), &pb.ReserveRequest{RequestId: "E01", Sku: "001-SKU", Quantity: 2}, grpc.Header(&header), grpc.Trailer(&trailer), grpc.WaitForReady(true))
	if err != nil {
		return err
	}
	if err := check(reply.GetRemaining() == 18, "正常扣减返回 %d", reply.GetRemaining()); err != nil {
		return err
	}
	if err := check(reflect.DeepEqual(header.Get("x-tenant"), []string{"course-m31"}) && reflect.DeepEqual(header.Get("x-rpc-owner"), []string{"inventory-handler"}) && reflect.DeepEqual(trailer.Get("x-course"), []string{"m31"}), "header/trailer 归属丢失: %v/%v", header, trailer); err != nil {
		return err
	}
	expected := []string{"client.outer.enter", "client.inner.enter", "server.outer.enter", "server.recovery.enter", "server.identity.enter", "server.inner.enter", "handler.enter", "handler.tenant=course-m31", "stock.commit", "server.inner.exit", "server.identity.exit", "server.recovery.exit", "server.outer.exit", "client.inner.exit", "client.outer.exit"}
	if actual := h.log.stages("E01"); !reflect.DeepEqual(expected, actual) {
		return fmt.Errorf("拦截器顺序错误: %v", actual)
	}
	h.log.add("E01", "ASSERT reply=18; metadata.owner=inventory-handler; interceptor.order=OK")
	_, err = h.client.Reserve(ownedContext(ctx), &pb.ReserveRequest{RequestId: "E02", Sku: "001-SKU", Quantity: 99})
	if err := check(status.Code(err) == codes.FailedPrecondition, "缺货 code=%s", status.Code(err)); err != nil {
		return err
	}
	details := status.Convert(err).Details()
	if len(details) != 1 {
		return fmt.Errorf("业务详情条数 %d", len(details))
	}
	conflict, ok := details[0].(*pb.Conflict)
	if err := check(ok && conflict.GetRequested() == 99 && conflict.GetAvailable() == 18, "Conflict 动态类型/数量不符合业务事实: %v", details); err != nil {
		return err
	}
	report.ConflictAvailable = conflict.GetAvailable()
	if err := check(h.service.writeCount("E02") == 0, "缺货仍产生写入"); err != nil {
		return err
	}
	h.log.add("E02", "ASSERT code=FailedPrecondition; detail(requested=99,available=18); writes=0")
	_, err = h.client.Reserve(ctx, &pb.ReserveRequest{RequestId: "E02-missing-owner", Sku: "001-SKU", Quantity: 1})
	if err := check(status.Code(err) == codes.Unauthenticated && h.service.writeCount("E02-missing-owner") == 0, "缺 metadata 未在 handler 前拒绝"); err != nil {
		return err
	}
	_, err = h.client.Reserve(ownedContext(ctx), &pb.ReserveRequest{RequestId: "E02-invalid", Sku: "001-SKU", Quantity: 0})
	if err := check(status.Code(err) == codes.InvalidArgument, "零数量未拒绝"); err != nil {
		return err
	}
	_, err = h.client.Reserve(ownedContext(ctx), &pb.ReserveRequest{RequestId: "E02-panic", Sku: "001-SKU", Quantity: 1, Mode: pb.ProcessingMode_PANIC_BEFORE_COMMIT})
	if err := check(status.Code(err) == codes.Internal && h.service.writeCount("E02-panic") == 0, "panic 未被当前 RPC goroutine recovery 边界转换"); err != nil {
		return err
	}
	stock, err := h.inspect(ctx)
	if err != nil {
		return err
	}
	if err := check(stock == 18, "拒绝/panic 后库存变化: %d", stock); err != nil {
		return err
	}
	h.log.add("E02", "ASSERT unauthenticated/invalid/panic bounded; next real Inspect=18")
	return nil
}

// rpcResult 是 client goroutine 交给实验 owner 的结果；channel buffer=1 防止 owner 已失败时发送者无法退出。
type rpcResult struct {
	reply *pb.ReserveReply
	err   error
}

// controlledWrite 把同一扣 2 件业务放到三种等待位置；只有这里创建 client goroutine，owner 负责取消和等待。
// manualCancel=true 在 started 屏障之后取消，其他实验由 400ms deadline 自动触发；不使用 sleep 猜 handler 时机。
func controlledWrite(ctx context.Context, h *harness, id string, mode pb.ProcessingMode, manualCancel bool) (writes int, err error) {
	p := h.service.installGate(id)
	var requestCtx context.Context
	var cancel context.CancelFunc
	if manualCancel {
		requestCtx, cancel = context.WithCancel(ctx)
	} else {
		requestCtx, cancel = context.WithTimeout(ctx, 400*time.Millisecond)
	}
	results := make(chan rpcResult, 1)
	callDone := make(chan struct{})
	go func() {
		reply, callErr := h.client.Reserve(ownedContext(requestCtx), &pb.ReserveRequest{RequestId: id, Sku: "001-SKU", Quantity: 2, Mode: mode})
		results <- rpcResult{reply, callErr}
		close(callDone)
	}()
	defer func() {
		// cancel 只是通知；release 保证坏 handler 也能结束；下面另等 client goroutine 确认退出。
		cancel()
		p.release()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		err = errors.Join(err, waitSignal(cleanupCtx, callDone, id+" client goroutine"))
	}()
	if err = waitSignal(ctx, p.started, id+" handler.started"); err != nil {
		return 0, err
	}
	if mode == pb.ProcessingMode_COMMIT_BEFORE_RESPONSE {
		if err = waitSignal(ctx, p.committed, id+" stock.committed"); err != nil {
			return 0, err
		}
	}
	if manualCancel {
		h.log.add(id, "client.cancel.notify")
		cancel()
	}
	var result rpcResult
	select {
	case result = <-results:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	wantCode := codes.DeadlineExceeded
	if manualCancel {
		wantCode = codes.Canceled
	}
	if err = check(status.Code(result.err) == wantCode && result.reply == nil, "%s client code=%s reply=%v", id, status.Code(result.err), result.reply); err != nil {
		return 0, err
	}
	h.log.add(id, "client.return."+wantCode.String())
	if mode == pb.ProcessingMode_DROP_CONTEXT_BEFORE_COMMIT {
		// 先证明超时前没有写入，再释放错误的独立 context；它会在 client 已返回之后真正扣库存。
		if err = check(h.service.writeCount(id) == 0, "坏例子释放 gate 前已有写入"); err != nil {
			return 0, err
		}
		h.log.add(id, "owner.release.after-client-error")
		p.release()
	}
	if err = waitSignal(ctx, p.done, id+" handler.done"); err != nil {
		return 0, err
	}
	writes = h.service.writeCount(id)
	wantWrites := 1
	if mode == pb.ProcessingMode_PROPAGATE_CONTEXT_BEFORE_COMMIT {
		wantWrites = 0
	}
	if err = check(writes == wantWrites, "%s 写入次数=%d want=%d", id, writes, wantWrites); err != nil {
		return 0, err
	}
	stock, err := h.inspect(ctx)
	if err != nil {
		return 0, err
	}
	h.log.add(id, fmt.Sprintf("ASSERT final.writes=%d; fresh.RPC.stock=%d; handler.done.observed", writes, stock))
	// done 后删除故障屏障，下一次正常请求有独立生命周期；request_id 写入记录仍保留。
	h.service.removeGate(id)
	return writes, nil
}

// runDeadlines 对比 ctx 丢失、提交前取消和提交后超时，再用同一 ID 实际重试证明写 RPC 没有自动幂等。
func runDeadlines(ctx context.Context, h *harness, report *Report) error {
	var err error
	report.LostContextWrites, err = controlledWrite(ctx, h, "E03-lost", pb.ProcessingMode_DROP_CONTEXT_BEFORE_COMMIT, false)
	if err != nil {
		return err
	}
	report.PropagatedWrites, err = controlledWrite(ctx, h, "E04-cancel", pb.ProcessingMode_PROPAGATE_CONTEXT_BEFORE_COMMIT, true)
	if err != nil {
		return err
	}
	if _, err = controlledWrite(ctx, h, "E04-deadline", pb.ProcessingMode_PROPAGATE_CONTEXT_BEFORE_COMMIT, false); err != nil {
		return err
	}
	if _, err = controlledWrite(ctx, h, "E05-committed", pb.ProcessingMode_COMMIT_BEFORE_RESPONSE, false); err != nil {
		return err
	}
	// 显式坏重试：原请求已经扣了 2 件，却把相同 ID 当作“没成功”再调用 NORMAL。
	// 这不是客户端库隐式重试；应用亲自发送第二个真实写 RPC，所以可精确归属额外扣减。
	reply, err := h.client.Reserve(ownedContext(ctx), &pb.ReserveRequest{RequestId: "E05-committed", Sku: "001-SKU", Quantity: 2})
	if err != nil {
		return err
	}
	report.CommittedWrites = h.service.writeCount("E05-committed")
	if err := check(report.CommittedWrites == 2 && reply.GetRemaining() == 12, "盲重试应双扣，writes=%d stock=%d", report.CommittedWrites, reply.GetRemaining()); err != nil {
		return err
	}
	h.log.add("E05-committed", "ASSERT blind.retry.same-ID writes=2; stock=12; no idempotency guarantee")
	return nil
}

// runStreams 依次验证半关闭、消费一条提前取消、不消费导致真实 Send 背压；所有流都有独立 ctx 上限。
func runStreams(ctx context.Context, h *harness, report *Report) error {
	if err := normalStream(ctx, h, report); err != nil {
		return err
	}
	if err := earlyStream(ctx, h); err != nil {
		return err
	}
	return backpressureStream(ctx, h, report)
}

// normalStream 中实验 owner 同时负责 Send/CloseSend 和 Recv；CloseSend 只关闭发送方向，不取消接收方向。
func normalStream(ctx context.Context, h *harness, report *Report) error {
	streamCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p := h.service.installWatch("E06-half-close")
	stream, err := h.client.Watch(ownedContext(streamCtx))
	if err != nil {
		return err
	}
	if err = stream.Send(&pb.WatchRequest{RunId: "E06-half-close", Sku: "001-SKU", Samples: 2}); err != nil {
		return err
	}
	if err = stream.CloseSend(); err != nil {
		return err
	}
	for {
		message, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		report.StreamReceived++
		if err = check(message.GetAvailable() == 12 && message.GetSequence() == int32(report.StreamReceived), "流消息顺序/快照错误: %v", message); err != nil {
			return err
		}
	}
	header, err := stream.Header()
	if err != nil {
		return err
	}
	if err = check(report.StreamReceived == 2 && reflect.DeepEqual(header.Get("x-tenant"), []string{"course-m31"}), "流 EOF/metadata 验收错误"); err != nil {
		return err
	}
	if err = waitSignal(ctx, p.done, "half-close handler.done"); err != nil {
		return err
	}
	h.log.add("E06-half-close", "ASSERT CloseSend→2 messages→Recv EOF; server.done.observed")
	return nil
}

// drainCanceled 等到终止 status；cancel 之后 Recv 仍可能先取出已经到达的缓冲消息，不应断言下一次立即报错。
// samples 上限 64 且 ctx 有界，额外最多 65 次 Recv 就必须收到终止结果。
func drainCanceled(stream grpc.BidiStreamingClient[pb.WatchRequest, pb.Stock]) error {
	for i := 0; i <= 64; i++ {
		_, err := stream.Recv()
		if err != nil {
			return check(status.Code(err) == codes.Canceled, "流取消终止 code=%s", status.Code(err))
		}
	}
	return errors.New("流取消后超过消息数量上限仍未终止")
}

// earlyStream 读取一条就退出业务消费；显式 cancel 终止当前 RPC，连接本身继续被后续实验复用。
func earlyStream(ctx context.Context, h *harness) error {
	streamCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p := h.service.installWatch("E06-early")
	stream, err := h.client.Watch(ownedContext(streamCtx))
	if err != nil {
		return err
	}
	if err = stream.Send(&pb.WatchRequest{RunId: "E06-early", Sku: "001-SKU", Samples: 4}); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if err = check(first.GetSequence() == 1, "提前退出前应收到第一条"); err != nil {
		return err
	}
	h.log.add("E06-early", "client.cancel.after-first-message")
	cancel()
	if err = drainCanceled(stream); err != nil {
		return err
	}
	if err = waitSignal(ctx, p.done, "early stream handler.done"); err != nil {
		return err
	}
	h.log.add("E06-early", "ASSERT final=Cancelled; server.done.observed; shared.Conn.reused")
	return nil
}

// backpressureStream 故意不 Recv：server 每条 Send 前记录 attempts，返回后记录 sent。
// owner 从真实已进入 Send 的信号起计时；150ms 内未返回是本次背压观察，不承诺所有机器阻塞在相同序号。
func backpressureStream(ctx context.Context, h *harness, report *Report) error {
	streamCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	p := h.service.installWatch("E06-backpressure")
	stream, err := h.client.Watch(ownedContext(streamCtx))
	if err != nil {
		return err
	}
	if err = stream.Send(&pb.WatchRequest{RunId: "E06-backpressure", Sku: "001-SKU", Samples: 64, PaddingBytes: 256 * 1024}); err != nil {
		return err
	}
	for i := 0; i < 64; i++ {
		var sequence int32
		select {
		case sequence = <-p.attempts:
		case <-streamCtx.Done():
			return fmt.Errorf("未观察到 Send 进入: %w", streamCtx.Err())
		}
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case sent := <-p.sent:
			timer.Stop()
			if err = check(sent == sequence, "Send 完成序号 %d != 进入 %d", sent, sequence); err != nil {
				return err
			}
		case <-timer.C:
			report.BlockedSend = sequence
		case <-streamCtx.Done():
			timer.Stop()
			return streamCtx.Err()
		}
		if report.BlockedSend != 0 {
			break
		}
	}
	if err = check(report.BlockedSend > 0, "64 条都返回，未观察到预期 HTTP/2 背压"); err != nil {
		return err
	}
	h.log.add("E06-backpressure", fmt.Sprintf("observer.Send(%d).not-returned-for-150ms; client.Recv.calls=0", report.BlockedSend))
	cancel() // 通知传输取消；服务端被阻塞的 Send 随后返回错误，handler 的 defer 关闭 done。
	if err = waitSignal(ctx, p.done, "backpressure stream handler.done"); err != nil {
		return err
	}
	if err = drainCanceled(stream); err != nil {
		return err
	}
	h.log.add("E06-backpressure", "ASSERT cancel→Send returns error→server.done; bounded memory/messages")
	return nil
}
