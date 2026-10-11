package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	runtimetrace "runtime/trace"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Result 属于实验 caller，包含 HTTP 实际状态与解析出的业务结果。
// 客户端 Err 为 canceled 只证明客户端不再等待；是否写入必须另查 Committed。
// Elapsed 在 client.Do 返回响应头/错误时取值：包括构造/传播与等待响应头，
// 不包括后面的 Body 解析、Close 或 span.End；它不是整次 request 函数生命周期。
// 请求构造失败的提前分支没有计时样本，Elapsed 为默认零。
type Result struct {
	Status  int
	Reply   Reply
	Err     error
	Elapsed time.Duration
}

type Environment struct {
	CapturedAt string `json:"captured_at"`
	GoVersion  string `json:"go_version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	CPUs       int    `json:"logical_cpus"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	OTel       string `json:"otel_version"`
	Limits     string `json:"limits"`
	Scope      string `json:"scope"`
}

type Summary struct {
	Environment                 Environment `json:"environment"`
	BadQueuedWhileIO            bool        `json:"bad_queued_while_io"`
	FixedCompletedBeforeRelease bool        `json:"fixed_completed_before_release"`
	UnknownCommitted            bool        `json:"unknown_committed"`
	CanceledNotDispatched       bool        `json:"canceled_not_dispatched"`
	CPUChecksum                 uint64      `json:"cpu_checksum"`
	CPUBadMillis                float64     `json:"cpu_bad_ms"`
	CPUFixedMillis              float64     `json:"cpu_fixed_ms"`
	CPUTimingScope              string      `json:"cpu_timing_scope"`
	DigestBadCalls              int         `json:"digest_bad_calls"`
	DigestFixedCalls            int         `json:"digest_fixed_calls"`
	Spans                       int         `json:"exported_local_spans"`
	PropagationEdges            int         `json:"validated_remote_parent_edges"`
	AsyncLinks                  int         `json:"validated_async_links"`
	Cleanup                     string      `json:"cleanup"`
}

// request 由实验 caller 调用，建立 driver client span 并通过真实 HTTP 注入上下文。
// 它拥有 response.Body；无论业务成功/失败都要读取并 Close。HTTP 状态与业务结果
// 分开存，不把 200 自动当作订单完成，也不把所有非 200 都当下游故障。
func request(ctx context.Context, s *Service, id, mode string) Result {
	ctx, span := s.Telemetry.Tracer.Start(ctx, "driver.checkout", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	started := time.Now()
	req, err := httpRequest(ctx, s.URL+"/checkout?mode="+mode)
	if err != nil {
		return Result{Err: err}
	}
	req.Header.Set("X-Request-ID", id)
	s.Telemetry.Propagator.Inject(ctx, propagation.HeaderCarrier(req.Header))
	response, err := s.client.Do(req)
	if err != nil {
		return Result{Err: err, Elapsed: time.Since(started)}
	}
	defer response.Body.Close()
	result := Result{Status: response.StatusCode, Elapsed: time.Since(started)}
	if mode == "panic" {
		_, result.Err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return result
	}
	result.Err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result.Reply)
	return result
}

// expectReply 同时检查协议结果和业务分类；真正提交的 map 由各实验另外独立检查。
func expectReply(result Result, status int, outcome Outcome) error {
	if result.Err != nil || result.Status != status || result.Reply.Outcome != outcome {
		return fmt.Errorf("response mismatch: status=%d outcome=%s error=%v", result.Status, result.Reply.Outcome, result.Err)
	}
	return nil
}

// fetchSample 从本次 Service 的真实 debug listener 读取文本/二进制响应，非 200 拒绝
// 保存为成功样本。调用者还需用 go tool pprof/trace 解析验证格式与源码含义。
func fetchSample(ctx context.Context, s *Service, path, out, filename string) error {
	req, err := httpGet(ctx, s.DebugURL+path)
	if err != nil {
		return err
	}
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("diagnostics endpoint returned %d", response.StatusCode)
	}
	file, err := os.Create(filepath.Join(out, filename))
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, io.LimitReader(response.Body, 16*1024*1024))
	return errors.Join(copyErr, file.Close())
}

// contentionTrial 是核心反例/修复对照：两轮使用相同 hold+normal 两种订单。
// bad 用 live stack 证明 B 在 Lock 等；fixed 让 B 完成后才释放 A，不以精确毫秒为验收。
// 返回前先取消/释放所有 driver 并等真实 Done，再 Close HTTP/audit；两类错误都保留。
func contentionTrial(ctx context.Context, variant, out string, t *Telemetry, summary *Summary) (err error) {
	s, err := NewService(ctx, variant, t)
	if err != nil {
		return err
	}
	drivers := &driverOwner{}
	defer func() {
		err = errors.Join(err, drivers.Close(), s.Close())
		if err == nil {
			_, inFlight, waiters, _ := s.Metrics.Snapshot()
			fmt.Printf("core variant=%s committed=2 in_flight=%d queue_waiters=%d proof=PASS\n", variant, inFlight, waiters)
		}
	}()
	holderID, fastID := variant+"-holder", variant+"-fast"
	holder := drivers.launch(ctx, s, holderID, "hold")
	if err = s.Await(ctx, holderID+":down_started"); err != nil {
		return err
	}
	fast := drivers.launch(ctx, s, fastID, "normal")
	if err = s.Await(ctx, fastID+":queue_entered"); err != nil {
		return err
	}
	if variant == "bad" {
		if _, err = waitingStack(ctx, "badCheckout", "lockSlow"); err != nil {
			return err
		}
		if err = fetchSample(ctx, s, "/debug/pprof/goroutine?debug=2", out, "goroutine-bad.txt"); err != nil {
			return err
		}
		if err = fetchSample(ctx, s, "/metrics", out, "metrics-bad-waiting.prom"); err != nil {
			return err
		}
		// 这是明确的观察窗口，为 block/mutex 形成容易读的样本。调度确认已经由
		// live stack 完成，timer 不承担“猜 worker 已经到了”的职责。
		window := time.NewTimer(180 * time.Millisecond)
		select {
		case <-window.C:
		case <-ctx.Done():
			window.Stop()
			return ctx.Err()
		}
		select {
		case <-fast.ResultReady():
			return errors.New("bad fast request unexpectedly finished before holder release")
		default:
		}
		if s.Committed(fastID) {
			return errors.New("bad queued order committed before lock acquisition")
		}
		summary.BadQueuedWhileIO = true
		s.Release(holderID)
		fastResult, takeErr := take(ctx, fast)
		if takeErr != nil {
			return takeErr
		}
		if err = expectReply(fastResult, 200, Success); err != nil {
			return err
		}
	} else {
		fastResult, takeErr := take(ctx, fast)
		if takeErr != nil {
			return takeErr
		}
		if err = expectReply(fastResult, 200, Success); err != nil {
			return err
		}
		if !s.Committed(fastID) || s.Committed(holderID) {
			return errors.New("fixed invariant: only fast order should be committed before release")
		}
		summary.FixedCompletedBeforeRelease = true
		if err = fetchSample(ctx, s, "/debug/pprof/goroutine?debug=2", out, "goroutine-fixed-before-release.txt"); err != nil {
			return err
		}
		s.Release(holderID)
	}
	holderResult, takeErr := take(ctx, holder)
	if takeErr != nil {
		return takeErr
	}
	if err = expectReply(holderResult, 200, Success); err != nil {
		return err
	}
	for _, id := range []string{holderID, fastID} {
		if err = s.Await(ctx, id+":finished"); err != nil {
			return err
		}
		if err = s.Await(ctx, id+":audit_finished"); err != nil {
			return err
		}
		if !s.Committed(id) {
			return fmt.Errorf("order %s not committed", id)
		}
	}
	counts, inFlight, waiters, dependencies := s.Metrics.Snapshot()
	if counts[Success] != 2 || inFlight != 0 || waiters != 0 || dependencies != 2 {
		return errors.New("contention metrics final invariant failed")
	}
	for _, name := range []string{"block", "mutex"} {
		if err = writeProfile(out, name, name+"-"+variant+".prof", 0); err != nil {
			return err
		}
	}
	if err = fetchSample(ctx, s, "/metrics", out, "metrics-"+variant+"-finished.prom"); err != nil {
		return err
	}
	return nil
}

// outcomeTrial 对真实 HTTP 测已知拒绝、真实 503、派发前取消、派发后结果未知与 panic。
// canceled 与 unknown 客户端都可能得到 context.Canceled，但权威副作用与 server 分类不同。
func outcomeTrial(ctx context.Context, out string, t *Telemetry, summary *Summary) (err error) {
	s, err := NewService(ctx, "fixed", t)
	if err != nil {
		return err
	}
	drivers := &driverOwner{}
	defer func() {
		err = errors.Join(err, drivers.Close(), s.Close())
		if err == nil {
			_, _, _, dependencies := s.Metrics.Snapshot()
			fmt.Printf("outcomes business_error=1 dependency_error=1 canceled=1 unknown=1 internal_error=1 dependency_samples=%d unknown_authority_committed=%t proof=PASS\n", dependencies, summary.UnknownCommitted)
		}
	}()
	for _, sample := range []struct {
		id, mode string
		status   int
		outcome  Outcome
	}{{"business-reject", "business", 409, BusinessError}, {"dependency-fail", "dependency", 503, DependencyError}} {
		result := request(ctx, s, sample.id, sample.mode)
		if err = expectReply(result, sample.status, sample.outcome); err != nil {
			return err
		}
		if err = s.Await(ctx, sample.id+":finished"); err != nil {
			return err
		}
		if s.Committed(sample.id) {
			return errors.New("rejected order should not commit")
		}
	}
	_, _, _, beforeDependencies := s.Metrics.Snapshot()
	canceled := drivers.launch(ctx, s, "admission-cancel", "cancel")
	if err = s.Await(ctx, "admission-cancel:queue_entered"); err != nil {
		return err
	}
	canceled.Cancel()
	result, takeErr := take(ctx, canceled)
	if takeErr != nil {
		return takeErr
	}
	if !errors.Is(result.Err, context.Canceled) {
		return fmt.Errorf("expected caller cancellation, got %v", result.Err)
	}
	if err = s.Await(ctx, "admission-cancel:finished"); err != nil {
		return err
	}
	_, _, _, afterDependencies := s.Metrics.Snapshot()
	if beforeDependencies != afterDependencies || s.Committed("admission-cancel") {
		return errors.New("canceled request unexpectedly dispatched or committed")
	}
	summary.CanceledNotDispatched = true
	unknown := drivers.launch(ctx, s, "response-lost", "unknown")
	if err = s.Await(ctx, "response-lost:committed"); err != nil {
		return err
	}
	unknown.Cancel()
	result, takeErr = take(ctx, unknown)
	if takeErr != nil {
		return takeErr
	}
	if !errors.Is(result.Err, context.Canceled) {
		return fmt.Errorf("unknown sample caller error=%v", result.Err)
	}
	for _, key := range []string{"response-lost:finished", "response-lost:down_canceled", "response-lost:down_finished"} {
		if err = s.Await(ctx, key); err != nil {
			return err
		}
	}
	summary.UnknownCommitted = s.Committed("response-lost")
	if !summary.UnknownCommitted {
		return errors.New("response loss sample did not leave authoritative commit")
	}
	panicResult := request(ctx, s, "panic-stack", "panic")
	if panicResult.Err != nil || panicResult.Status != 500 {
		return errors.New("panic boundary did not return 500")
	}
	if err = s.Await(ctx, "panic-stack:finished"); err != nil {
		return err
	}
	counts, inFlight, waiters, dependencies := s.Metrics.Snapshot()
	for _, o := range []Outcome{BusinessError, DependencyError, Canceled, Unknown, InternalError} {
		if counts[o] != 1 {
			return fmt.Errorf("outcome counter %s=%d want 1", o, counts[o])
		}
	}
	if counts[Success] != 0 || inFlight != 0 || waiters != 0 || dependencies != 3 {
		return errors.New("outcome metrics/cleanup invariant failed")
	}
	if err = fetchSample(ctx, s, "/metrics", out, "metrics-outcomes.prom"); err != nil {
		return err
	}
	return nil
}

// cpuTrial 在两次独立 Start/Stop 范围内发真实 HTTP CPU 请求。StopCPUProfile 会等
// 所有 buffered profile 写完，然后才关闭文件。cpu_fixed 可能短到没有业务 CPU 样本，
// 这代表观察窗口里样本不足；工作量减少还由 calls 和 checksum 不变量独立证明。
func cpuTrial(ctx context.Context, out string, t *Telemetry, summary *Summary) (err error) {
	s, err := NewService(ctx, "fixed", t)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	var first uint64
	for _, mode := range []string{"cpu_bad", "cpu_fixed"} {
		file, createErr := os.Create(filepath.Join(out, mode+".prof"))
		if createErr != nil {
			return createErr
		}
		if startErr := pprof.StartCPUProfile(file); startErr != nil {
			_ = file.Close()
			return startErr
		}
		result := request(ctx, s, mode, mode)
		pprof.StopCPUProfile()
		if closeErr := file.Close(); closeErr != nil {
			return closeErr
		}
		if err = expectReply(result, 200, Success); err != nil {
			return err
		}
		if err = s.Await(ctx, mode+":finished"); err != nil {
			return err
		}
		if mode == "cpu_bad" {
			first = result.Reply.Value
			summary.CPUBadMillis = float64(result.Elapsed) / float64(time.Millisecond)
		} else {
			summary.CPUFixedMillis = float64(result.Elapsed) / float64(time.Millisecond)
			if result.Reply.Value != first {
				return errors.New("CPU repair changed checksum")
			}
		}
	}
	bad, calls := Digest(false, 5)
	fixed, fixedCalls := Digest(true, 5)
	if bad != fixed || calls != 5 || fixedCalls != 1 {
		return errors.New("digest work-count invariant failed")
	}
	summary.CPUChecksum = first
	summary.DigestBadCalls = DigestIterations
	summary.DigestFixedCalls = 1
	summary.CPUTimingScope = "Result.Elapsed from after driver span Start through client.Do response headers/error; excludes response body decode/Close and span.End"
	fmt.Printf("cpu same_checksum=%t digest_calls=%d->1 response_header_ms=%.3f->%.3f (sample only)\n", true, DigestIterations, summary.CPUBadMillis, summary.CPUFixedMillis)
	return nil
}

// validateSpans 使用结束后的元数据查真实 parent；名字相同不等于同一条链路。
// reserve.server 每条边必须能按 Parent 找到 reserve.client，trace ID 相同且为 remote。
// audit.async 必须是新 trace 的 root，同时用 Link 指向已存在的 checkout.server。
func validateSpans(records []spanRecord) (int, int, error) {
	byID := make(map[string]spanRecord)
	for _, r := range records {
		byID[r.SpanID] = r
	}
	edges, links := 0, 0
	for _, r := range records {
		if r.Name == "checkout.server" {
			parent, ok := byID[r.Parent]
			if !ok || parent.Name != "driver.checkout" || parent.TraceID != r.TraceID || !r.RemoteParent {
				return 0, 0, errors.New("frontend HTTP trace propagation invariant failed")
			}
			edges++
		}
		if r.Name == "reserve.server" {
			parent, ok := byID[r.Parent]
			if !ok || parent.Name != "reserve.client" || parent.TraceID != r.TraceID || !r.RemoteParent {
				return 0, 0, errors.New("real HTTP trace propagation invariant failed")
			}
			edges++
		}
		if r.Name == "audit.async" {
			if r.Parent != "0000000000000000" || len(r.Links) != 1 {
				return 0, 0, errors.New("async span is not linked new root")
			}
			found := false
			for _, parent := range records {
				if parent.Name == "checkout.server" && r.Links[0] == parent.TraceID+"/"+parent.SpanID && parent.TraceID != r.TraceID {
					found = true
					break
				}
			}
			if !found {
				return 0, 0, errors.New("async span link not backed by original request")
			}
			links++
		}
	}
	if edges == 0 || links == 0 {
		return 0, 0, errors.New("missing trace observations")
	}
	return edges, links, nil
}

// countExported 读取 stdouttrace 实际落盘的 JSON 流，与 OnEnd 旁路记录数对照。
// HTTP 200、Provider 创建或 span ID 存在都不足以证明 exporter 写入成功。
func countExported(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	count := 0
	for {
		var span map[string]json.RawMessage
		err = decoder.Decode(&span)
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		if len(span["Name"]) == 0 || len(span["SpanContext"]) == 0 {
			return count, errors.New("exported span schema missing identifiers")
		}
		count++
	}
}

// childLeakExperiment 用当前已经编译的 demo 二进制，不能再运行一个不受控 go run。
// 子进程 deadline=5 秒；不可达泄漏只存在子进程内，退出后父进程没有遗留 worker。
func childLeakExperiment(ctx context.Context, out, kind string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	childCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(childCtx, executable, "-leak-child", kind)
	result, err := command.CombinedOutput()
	if writeErr := os.WriteFile(filepath.Join(out, "leak-"+kind+".txt"), result, 0600); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return fmt.Errorf("leak child %s: %w: %s", kind, err, string(result))
	}
	return nil
}

// Run 是 cmd/demo 的总 owner，所有采样与服务都有上限，错误直接返回到 main。
// 各 profile 开关是进程全局；本课是独立程序，所以可以临时启用并恢复。不要把相同
// 操作嵌进生产每个 handler，否则会改变整个进程采样，CPU/trace 也只能各有一个会话。
func Run(ctx context.Context, out string) (summary Summary, err error) {
	if err = os.MkdirAll(out, 0700); err != nil {
		return summary, err
	}
	summary.Environment = Environment{CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), OTel: "v1.47.0", Limits: "work overall 45s; HTTP 4s; driver cleanup fresh 3s per owner; server cleanup fresh 3s; child 5s; 400000 x 4KiB digest; retained array 8MiB; 3 listeners per trial; 2 concurrent requests", Scope: "local real HTTP and local stdouttrace exporter; possible concurrent course builds; timings are this sample, no cloud backend or performance SLA"}
	logFile, err := os.Create(filepath.Join(out, "requests.jsonl"))
	if err != nil {
		return summary, err
	}
	defer func() { err = errors.Join(err, logFile.Close()) }()
	spanFile, err := os.Create(filepath.Join(out, "otel-spans.jsonl"))
	if err != nil {
		return summary, err
	}
	defer func() { err = errors.Join(err, spanFile.Close()) }()
	telemetry, err := NewTelemetry(logFile, spanFile)
	if err != nil {
		return summary, err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = errors.Join(err, telemetry.Provider.Shutdown(shutdownCtx))
	}()
	oldMutexRate := runtime.SetMutexProfileFraction(1)
	defer runtime.SetMutexProfileFraction(oldMutexRate)
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)
	oldMemRate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = oldMemRate }()
	traceFile, err := os.Create(filepath.Join(out, "runtime-trace.out"))
	if err != nil {
		return summary, err
	}
	if err = runtimetrace.Start(traceFile); err != nil {
		_ = traceFile.Close()
		return summary, err
	}
	// trace 停止必须在文件关闭前；把 Stop 包在函数 defer，让任一 core 失败也能完成格式。
	err = func() error {
		defer runtimetrace.Stop()
		if trialErr := contentionTrial(ctx, "bad", out, telemetry, &summary); trialErr != nil {
			return trialErr
		}
		return contentionTrial(ctx, "fixed", out, telemetry, &summary)
	}()
	err = errors.Join(err, traceFile.Close())
	if err != nil {
		return summary, err
	}
	if err = outcomeTrial(ctx, out, telemetry, &summary); err != nil {
		return summary, err
	}
	if err = cpuTrial(ctx, out, telemetry, &summary); err != nil {
		return summary, err
	}
	if err = heapExperiment(out); err != nil {
		return summary, err
	}
	for _, kind := range []string{"unreachable", "reachable"} {
		if err = childLeakExperiment(ctx, out, kind); err != nil {
			return summary, err
		}
	}
	// 此时每个 Trial 已分别 join driver Done 与 Service 的 HTTP/audit，元数据不再更新。
	records := telemetry.capture.snapshot()
	summary.PropagationEdges, summary.AsyncLinks, err = validateSpans(records)
	if err != nil {
		return summary, err
	}
	if err = spanFile.Sync(); err != nil {
		return summary, err
	}
	summary.Spans, err = countExported(filepath.Join(out, "otel-spans.jsonl"))
	if err != nil {
		return summary, err
	}
	if summary.Spans != len(records) {
		return summary, errors.New("exported file count differs from ended spans")
	}
	if err = writeJSON(filepath.Join(out, "span-relationships.json"), records); err != nil {
		return summary, err
	}
	summary.Cleanup = "each async driver child canceled/gate released and independent Done joined with fresh cleanup budget; all HTTP handlers/listeners and owned audit goroutines separately joined; transports idle connections closed; CPU/trace stopped; profile settings restored on return; bounded child processes exited"
	if err = writeJSON(filepath.Join(out, "report.json"), summary); err != nil {
		return summary, err
	}
	fmt.Printf("trace exported_local=%d remote_parent_edges=%d linked_async_roots=%d proof=PASS\n", summary.Spans, summary.PropagationEdges, summary.AsyncLinks)
	fmt.Printf("artifacts=%s cleanup=PASS\n", out)
	return summary, nil
}

// writeJSON 只写合成本地证据，不序列化请求头/配置/凭据；缩进便于直接比对。
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
