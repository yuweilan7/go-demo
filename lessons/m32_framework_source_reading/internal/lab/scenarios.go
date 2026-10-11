// Package lab 是有界实验驱动；业务/service/root均不会反向导入它。
package lab

import (
	"context"
	"errors"
	"fmt"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	pb "go-demo/lessons/m32_framework_source_reading/gen/inventoryv1"
	"go-demo/lessons/m32_framework_source_reading/internal/app"
	"go-demo/lessons/m32_framework_source_reading/internal/biz"
	"go-demo/lessons/m32_framework_source_reading/internal/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Summary 给CLI和独立测试返回业务事实值，而不是让调用方只搜索PASS字符串。
type Summary struct {
	FinalStock                            int32
	LostWrites, CancelWrites, RetryWrites int
	StreamMessages                        int
	HandlersJoined, ConnectionsClosed     bool
}

// Run 被cmd/demo或测试调用。总ctx限制请求实验；所有defer使用新Background收尾，不让请求超时阻止join。
// 主错误与客户端/root收尾错误Join，多个原因不会被后一个Close错误覆盖。没有sleep猜测调度。
func Run(ctx context.Context, out io.Writer) (summary Summary, err error) {
	j := trace.New(out)
	r, err := app.New(app.DefaultConfig(), j)
	if err != nil {
		return summary, err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	r.Start()
	c, err := newClients(ctx, r, j, "m32")
	if err != nil {
		return summary, err
	}
	defer func() { err = errors.Join(err, c.close()) }()
	fmt.Fprintln(out, "E01 generated HTTP/gRPC → real Kratos → usecase → repository")
	for _, item := range []struct {
		id        string
		qty, want int32
		http      bool
	}{{"E01-grpc", 2, 18, false}, {"E01-http", 1, 17, true}} {
		req := request(item.id, item.qty, pb.ProcessingMode_NORMAL)
		var reply *pb.ReserveReply
		if item.http {
			reply, err = c.http.Reserve(ctx, req)
		} else {
			reply, err = c.grpc.Reserve(ctx, req)
		}
		if err != nil {
			return summary, err
		}
		if reply.GetRemaining() != item.want || reply.GetRequestId() != item.id {
			return summary, fmt.Errorf("E01完整reply不符: %v", reply)
		}
		if err = checkChain(j.Lines(), item.id); err != nil {
			return summary, err
		}
	}
	if err = verifyStock(ctx, c, r, 17); err != nil {
		return summary, err
	}

	fmt.Fprintln(out, "E02 business error / explicit recovery / panic does not rollback")
	_, err = c.grpc.Reserve(ctx, request("E02-stock-grpc", 99, pb.ProcessingMode_NORMAL))
	if status.Code(err) != codes.FailedPrecondition {
		return summary, fmt.Errorf("库存error grpc=%v", err)
	}
	detailOK := false
	for _, detail := range status.Convert(err).Details() {
		if conflict, ok := detail.(*pb.Conflict); ok && conflict.GetRequested() == 99 && conflict.GetAvailable() == 17 {
			detailOK = true
		}
	}
	if !detailOK {
		return summary, errors.New("M31 typed Conflict未保留")
	}
	_, err = c.http.Reserve(ctx, request("E02-stock-http", 99, pb.ProcessingMode_NORMAL))
	shortage := kerrors.FromError(err)
	if shortage == nil || shortage.Code != 412 || shortage.Reason != "STOCK_INSUFFICIENT" || shortage.Metadata["available"] != "17" {
		return summary, fmt.Errorf("HTTP业务错误详情不符: %v", err)
	}
	_, err = c.grpc.Reserve(ctx, request("E02-input", 0, pb.ProcessingMode_NORMAL))
	if status.Code(err) != codes.InvalidArgument {
		return summary, fmt.Errorf("invalid code: %v", err)
	}
	missing := request("E02-missing", 1, pb.ProcessingMode_NORMAL)
	missing.Sku = "absent"
	_, err = c.grpc.Reserve(ctx, missing)
	if status.Code(err) != codes.NotFound {
		return summary, fmt.Errorf("missing code: %v", err)
	}
	unauth, buildErr := newClients(ctx, r, j, "")
	if buildErr != nil {
		return summary, buildErr
	}
	_, authErr := unauth.grpc.Reserve(ctx, request("E02-auth", 1, pb.ProcessingMode_NORMAL))
	closeErr := unauth.close()
	if status.Code(authErr) != codes.Unauthenticated {
		return summary, errors.Join(fmt.Errorf("auth code: %v", authErr), closeErr)
	}
	if closeErr != nil {
		return summary, closeErr
	}
	_, err = c.grpc.Reserve(ctx, request("E02-panic-before", 1, pb.ProcessingMode_PANIC_BEFORE_COMMIT))
	if status.Code(err) != codes.Internal || kerrors.Reason(err) != "UNKNOWN" {
		return summary, fmt.Errorf("recovery code: %v", err)
	}
	_, err = c.http.Reserve(ctx, request("E02-panic-http", 1, pb.ProcessingMode_PANIC_BEFORE_COMMIT))
	if kerrors.Code(err) != 500 || kerrors.Reason(err) != "UNKNOWN" {
		return summary, fmt.Errorf("HTTP recovery: %v", err)
	}
	if err = verifyStock(ctx, c, r, 17); err != nil {
		return summary, err
	}
	// 必须在错误请求真实执行后逐ID读取仓储事实，不能在请求前看到0就声称“错误无写入”。
	for _, id := range []string{"E02-stock-grpc", "E02-stock-http", "E02-input", "E02-missing", "E02-auth", "E02-panic-before", "E02-panic-http"} {
		_, writes, _ := r.Repository.Snapshot(id)
		if writes != 0 {
			return summary, fmt.Errorf("错误请求%s仍写入%d次", id, writes)
		}
	}
	_, err = c.grpc.Reserve(ctx, request("E02-panic-after", 1, pb.ProcessingMode_PANIC_AFTER_COMMIT))
	if status.Code(err) != codes.Internal {
		return summary, fmt.Errorf("panic after: %v", err)
	}
	if err = verifyStock(ctx, c, r, 16); err != nil {
		return summary, err
	}
	_, panicWrites, _ := r.Repository.Snapshot("E02-panic-after")
	if panicWrites != 1 {
		return summary, fmt.Errorf("panic-after写入次数=%d", panicWrites)
	}
	j.Add("E02", "ASSERT shortage=FailedPrecondition/412 typed-detail=true panic-before-writes=0 panic-after-writes=1 stock=16")

	fmt.Fprintln(out, "E03 lost context: client deadline first, server commit after gate release")
	summary.LostWrites, err = lostContext(ctx, c, r, j)
	if err != nil {
		return summary, err
	}
	if err = verifyStock(ctx, c, r, 14); err != nil {
		return summary, err
	}

	fmt.Fprintln(out, "E04 propagate context: cancel / server timeout gRPC and HTTP")
	summary.CancelWrites, err = honorContext(ctx, c, r, j)
	if err != nil {
		return summary, err
	}
	if err = verifyStock(ctx, c, r, 14); err != nil {
		return summary, err
	}

	fmt.Fprintln(out, "E05 committed response lost / explicit retry is another stock deduction")
	summary.RetryWrites, err = retryAfterCommit(ctx, c, r, j)
	if err != nil {
		return summary, err
	}
	if err = verifyStock(ctx, c, r, 10); err != nil {
		return summary, err
	}
	summary.FinalStock = 10

	fmt.Fprintln(out, "E06 retained M31 bidi stream: CloseSend → two replies → EOF")
	summary.StreamMessages, err = smallStream(ctx, c)
	if err != nil {
		return summary, err
	}
	j.Add("E06", fmt.Sprintf("ASSERT messages=%d stock=10 final=EOF", summary.StreamMessages))
	// 所有任务先joined，才Close连接；不会靠关闭共享连接来假装某个调用的defer已经完成。
	if err = c.close(); err != nil {
		return summary, err
	}
	if c.grpcConn.GetState().String() != "SHUTDOWN" {
		return summary, errors.New("ClientConn.Close后未Shutdown")
	}
	if err = r.Close(); err != nil {
		return summary, err
	}
	summary.ConnectionsClosed = true
	summary.HandlersJoined = r.Active() == 0
	_, _, closed := r.Repository.Snapshot("")
	if !closed || !summary.HandlersJoined {
		return summary, errors.New("核心服务owner未最终收尾")
	}
	fmt.Fprintln(out, "E07 Stop notification / forced HTTP close / active handler final defer")
	if err = StopExperiment(ctx, out); err != nil {
		return summary, err
	}
	fmt.Fprintf(out, "PASS stock=%d lost_ctx_writes=%d propagated_writes=%d committed_and_retry_writes=%d stream_messages=%d handlers_joined=%t connections_closed=%t\n", summary.FinalStock, summary.LostWrites, summary.CancelWrites, summary.RetryWrites, summary.StreamMessages, summary.HandlersJoined, summary.ConnectionsClosed)
	return summary, nil
}

func request(id string, quantity int32, mode pb.ProcessingMode) *pb.ReserveRequest {
	return &pb.ReserveRequest{RequestId: id, Sku: biz.SKU, Quantity: quantity, Mode: mode}
}

// waitSignal只观察屏障；它不创建任务也不负责释放门。每个调用都有外层实验ctx上限。
func waitSignal(ctx context.Context, done <-chan struct{}, name string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待%s: %w", name, ctx.Err())
	}
}

// joinTask由创建task的实验调用，使用新的Background 3秒预算；即使原请求Canceled，也要等待最后defer。
func joinTask(task *app.Task) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return task.Join(ctx)
}

// verifyStock用fresh真实HTTP Inspect以及仓储独立快照同时核对：协议reply和业务副作用必须一致。
func verifyStock(ctx context.Context, c *clients, r *app.Root, want int32) error {
	stock, err := c.http.Inspect(ctx, &pb.InspectRequest{Sku: biz.SKU})
	if err != nil {
		return err
	}
	available, _, closed := r.Repository.Snapshot("")
	if stock.GetSku() != biz.SKU || stock.GetAvailable() != want || available != want || closed {
		return fmt.Errorf("库存事实want=%d reply=%v snapshot=%d closed=%t", want, stock, available, closed)
	}
	return nil
}

// checkChain是沿实际日志验证单个请求顺序的独立断言；并行请求之间没有固定全序承诺。
func checkChain(lines []string, id string) error {
	expected := []string{"client.enter", "server.enter", "usecase.enter tenant=m32", "repository.commit", "server.exit", "client.exit"}
	next := 0
	for _, line := range lines {
		if strings.Contains(line, "["+id+"]") && next < len(expected) && strings.Contains(line, expected[next]) {
			next++
		}
	}
	if next != len(expected) {
		return fmt.Errorf("%s链只走到%d/%d", id, next, len(expected))
	}
	return nil
}

// lostContext用100ms真实client deadline与已注册Started屏障制造“客户端结束，业务仍等待”的反例。
// defer保证任何提前失败也cancel→Release→Join客户端任务→等业务Done，不留下坏例后台副作用。
func lostContext(ctx context.Context, c *clients, r *app.Root, j *trace.Journal) (writes int, err error) {
	id := "E03-lost"
	p := r.Usecase.Install(id)
	reqCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	task := app.StartTask(func() error {
		defer cancel()
		_, callErr := c.grpc.Reserve(reqCtx, request(id, 2, pb.ProcessingMode_DROP_CONTEXT_BEFORE_COMMIT))
		return callErr
	}, nil)
	defer func() {
		cancel()
		p.Release()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		err = errors.Join(err, joinExpected(task, cleanupCtx, "grpc-timeout"))
		err = errors.Join(err, waitSignal(cleanupCtx, p.Done, "lost业务Done"))
		r.Usecase.Remove(id)
	}()
	if err = waitSignal(ctx, p.Started, "lost started"); err != nil {
		return 0, err
	}
	callErr := joinTask(task)
	if status.Code(callErr) != codes.DeadlineExceeded {
		return 0, fmt.Errorf("lost client error=%v", callErr)
	}
	// 预期的协议错误已经独立断言，不作为本实验失败；defer的第二次Join同一task要同样分类。
	select {
	case <-p.Done:
		return 0, errors.New("坏例竟在release之前完成")
	default:
	}
	p.Release()
	if err = waitSignal(ctx, p.Done, "lost actual business exit"); err != nil {
		return 0, err
	}
	_, writes, _ = r.Repository.Snapshot(id)
	if writes != 1 {
		return 0, fmt.Errorf("lost writes=%d", writes)
	}
	j.Add(id, "ASSERT client=DeadlineExceeded actual-writes=1 tenant-lost=true")
	return writes, nil
}

// honorContext复用同一个业务等待点，分别主动cancel、服务器200ms timeout(gRPC/HTTP)。
// 参数是框架client；没有用mock宣称context传播成功，最终仓储writes必须为0。
func honorContext(ctx context.Context, c *clients, r *app.Root, j *trace.Journal) (int, error) {
	for _, kind := range []string{"cancel", "grpc-timeout", "http-timeout"} {
		if err := oneHonored(ctx, c, r, j, kind); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// oneHonored拥有一条客户端goroutine、cancel和门。失败清理释放门时可能与ctx同时ready，
// 但好版本Commit仍锁内检查ctx.Err，不能在取消已观察后把副作用当成功。
func oneHonored(ctx context.Context, c *clients, r *app.Root, j *trace.Journal, kind string) (err error) {
	id := "E04-" + kind
	p := r.Usecase.Install(id)
	reqCtx, cancel := context.WithCancel(ctx)
	task := app.StartTask(func() error {
		defer cancel()
		req := request(id, 2, pb.ProcessingMode_PROPAGATE_CONTEXT_BEFORE_COMMIT)
		if kind == "http-timeout" {
			_, callErr := c.http.Reserve(reqCtx, req)
			return callErr
		}
		_, callErr := c.grpc.Reserve(reqCtx, req)
		return callErr
	}, nil)
	defer func() {
		cancel()
		p.Release()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		err = errors.Join(err, joinExpected(task, cleanupCtx, kind))
		err = errors.Join(err, waitSignal(cleanupCtx, p.Done, "honored业务Done"))
		r.Usecase.Remove(id)
	}()
	if err = waitSignal(ctx, p.Started, id+" started"); err != nil {
		return err
	}
	if kind == "cancel" {
		cancel()
	}
	if err = joinExpected(task, ctx, kind); err != nil {
		return err
	}
	if err = waitSignal(ctx, p.Done, id+" done"); err != nil {
		return err
	}
	_, writes, _ := r.Repository.Snapshot(id)
	if writes != 0 {
		return fmt.Errorf("%s writes=%d", id, writes)
	}
	j.Add(id, "ASSERT writes=0 actual-business-Done=true")
	return nil
}

// joinExpected保留真正join超时等收尾错误，只把该实验已明确期待的协议失败分类为成功观察。
func joinExpected(task *app.Task, ctx context.Context, kind string) error {
	err := task.Join(ctx)
	if !task.Finished() {
		return err
	}
	if kind == "cancel" && status.Code(err) == codes.Canceled {
		return nil
	}
	if kind == "http-timeout" && kerrors.Code(err) == 504 {
		return nil
	}
	if kind != "cancel" && kind != "http-timeout" && status.Code(err) == codes.DeadlineExceeded {
		return nil
	}
	return fmt.Errorf("%s期望协议错误不符: %w", kind, err)
}

// retryAfterCommit先等真实Committed，随后让100msclient deadline丢响应。第二次同ID是显式应用重试，
// 不是断言框架默认重试生效；writes=2/库存再减证明ID只有追踪能力，幂等须业务设计。
func retryAfterCommit(ctx context.Context, c *clients, r *app.Root, j *trace.Journal) (writes int, err error) {
	id := "E05-committed"
	p := r.Usecase.Install(id)
	reqCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	task := app.StartTask(func() error {
		defer cancel()
		_, callErr := c.grpc.Reserve(reqCtx, request(id, 2, pb.ProcessingMode_COMMIT_BEFORE_RESPONSE))
		return callErr
	}, nil)
	defer func() {
		cancel()
		p.Release()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		err = errors.Join(err, joinExpected(task, cleanupCtx, "grpc-timeout"))
		err = errors.Join(err, waitSignal(cleanupCtx, p.Done, "committed业务Done"))
		r.Usecase.Remove(id)
	}()
	if err = waitSignal(ctx, p.Committed, "真实commit"); err != nil {
		return 0, err
	}
	if err = joinExpected(task, ctx, "grpc-timeout"); err != nil {
		return 0, err
	}
	if err = waitSignal(ctx, p.Done, "after commit done"); err != nil {
		return 0, err
	}
	r.Usecase.Remove(id)
	reply, err := c.grpc.Reserve(ctx, request(id, 2, pb.ProcessingMode_NORMAL))
	if err != nil {
		return 0, err
	}
	_, writes, _ = r.Repository.Snapshot(id)
	if writes != 2 || reply.GetRemaining() != 10 {
		return 0, fmt.Errorf("重试事实writes=%d reply=%v", writes, reply)
	}
	j.Add(id, "ASSERT timeout-did-not-rollback=true explicit-retry-writes=2 stock=10")
	return writes, nil
}

// smallStream由Run同步拥有整个流，不启动业务goroutine；取消defer与Root.Close共同确保早退也结束服务器Recv。
// CloseSend只关闭发送半边，随后读完两条Stock和EOF才算正常协议结束。
func smallStream(ctx context.Context, c *clients) (messages int, err error) {
	streamCtx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(ctx, "x-course-tenant", "m32"), 2*time.Second)
	defer cancel()
	stream, err := c.grpc.Watch(streamCtx)
	if err != nil {
		return 0, err
	}
	if err = stream.Send(&pb.WatchRequest{RunId: "m32-stream", Sku: biz.SKU, Samples: 2}); err != nil {
		return 0, err
	}
	if err = stream.CloseSend(); err != nil {
		return 0, err
	}
	for {
		reply, receiveErr := stream.Recv()
		if receiveErr == io.EOF {
			break
		}
		if receiveErr != nil {
			return messages, receiveErr
		}
		messages++
		if reply.GetSequence() != int32(messages) || reply.GetAvailable() != 10 {
			return messages, fmt.Errorf("stream reply=%v", reply)
		}
	}
	if messages != 2 {
		return messages, fmt.Errorf("stream messages=%d", messages)
	}
	return messages, nil
}

// StopExperiment是独立真实HTTP fixture，故意把outer filter的尾部defer关住。
// 正常与所有提前错误路径都有一个defer owner释放两道门并独立join client/Stop caller/root。
// 它证明Result、App.Stop返回、App.Run返回、TCP关闭、handler退出是不同状态。
func StopExperiment(ctx context.Context, out io.Writer) (err error) {
	j := trace.New(out)
	tail := app.NewTail()
	cfg := app.DefaultConfig()
	cfg.Tail = tail
	r, err := app.New(cfg, j)
	if err != nil {
		return err
	}
	r.Start()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	raw := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	requestCtx, requestCancel := context.WithCancel(ctx)
	clientTask := app.StartTask(func() error {
		defer requestCancel()
		req, buildErr := http.NewRequestWithContext(requestCtx, "GET", "http://"+r.HTTPAddress+"/owned-tail", nil)
		if buildErr != nil {
			return buildErr
		}
		response, callErr := raw.Do(req)
		if callErr != nil {
			return callErr
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 32))
		if readErr != nil {
			return readErr
		}
		if response.StatusCode != 200 || string(body) != "tail" {
			return errors.New("tail reply不符")
		}
		return nil
	}, nil)
	callerTail := make(chan struct{})
	var stopTask *app.Task
	released := false
	defer func() {
		requestCancel()
		tail.Release()
		if !released {
			close(callerTail)
			released = true
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cleanupCancel()
		if stopTask != nil {
			err = errors.Join(err, stopTask.Join(cleanupCtx))
		}
		err = errors.Join(err, clientTask.Join(cleanupCtx), r.Close())
		transport.CloseIdleConnections()
	}()
	if err = waitSignal(ctx, tail.Returning, "handler返回前defer屏障"); err != nil {
		return err
	}
	if err = clientTask.Join(ctx); err != nil {
		return err
	}
	stopTask = app.StartTask(r.StopNotification, callerTail)
	select {
	case preview := <-stopTask.Result:
		if preview != nil {
			return preview
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if stopTask.Finished() {
		return errors.New("Stop caller结果被误当最终Done")
	}
	j.Add("E07", "OBSERVE App.Stop.result=nil close-caller.Done=false handler.active=1")
	if err = r.JoinFramework(ctx); err != nil {
		return err
	}
	if err = assertTCPStopped(ctx, r); err != nil {
		return err
	}
	_, _, closed := r.Repository.Snapshot("")
	select {
	case <-tail.Done:
		return errors.New("tail尚未release却done")
	default:
	}
	if r.Active() != 1 || closed {
		return fmt.Errorf("强制TCP close后active=%d repo.closed=%t", r.Active(), closed)
	}
	j.Add("E07", "OBSERVE App.Run.Done=true transport.Start.Done=true TCP.closed=true handler.Done=false repo.closed=false")
	close(callerTail)
	released = true
	if err = stopTask.Join(ctx); err != nil {
		return err
	}
	if err = r.Close(); err != nil {
		return err
	}
	if err = waitSignal(ctx, tail.Done, "handler最后defer"); err != nil {
		return err
	}
	if r.Active() != 0 {
		return errors.New("Close未join handler")
	}
	j.Add("E07", "ASSERT caller.Done=true handler.Done=true active=0 repository.closed=true all-owned-tasks-joined=true")
	return nil
}

// assertTCPStopped在已经join服务Start之后，用真实TCP connect核对两个自有listener确实停止接收。
// 若还能建连，立即Close那个诊断socket再报错；不扫描端口，只接触该fixture记录的两个地址。
func assertTCPStopped(ctx context.Context, r *app.Root) error {
	for _, address := range []string{r.HTTPAddress, r.GRPCAddress} {
		dialer := net.Dialer{Timeout: 200 * time.Millisecond}
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			return errors.Join(fmt.Errorf("Stop后自有listener仍接受TCP: %s", address), connection.Close())
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}
