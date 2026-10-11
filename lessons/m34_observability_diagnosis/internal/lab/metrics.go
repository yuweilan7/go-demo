package lab

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Outcome 是业务边界的有限分类。HTTP 409 是已知业务拒绝；调用已经发出后
// 响应丢失是 unknown；等待入场时取消是 canceled，三者不能混成“下游错误”。
type Outcome string

const (
	Success         Outcome = "success"
	BusinessError   Outcome = "business_error"
	Canceled        Outcome = "canceled"
	Unknown         Outcome = "unknown"
	DependencyError Outcome = "dependency_error"
	InternalError   Outcome = "internal_error"
)

var outcomes = []Outcome{Success, BusinessError, Canceled, Unknown, DependencyError, InternalError}
var boundaries = []float64{0.001, 0.01, 0.05, 0.25, 1}

// histogram 保存整个进程本次运行的累计样本；buckets[i] 是 <= boundaries[i]
// 的累计计数。它没有自己滚动的五分钟窗口，窗口需要两次 scrape 的差或 rate。
type histogram struct {
	buckets [5]uint64
	count   uint64
	sum     float64
}

// observe 由持有 Metrics.mu 的调用者调用，参数单位是秒。
// 每个样本会落入多个累计 bucket，不能把这些 bucket 相加当作请求总数。
func (h *histogram) observe(d time.Duration) {
	seconds := d.Seconds()
	h.count++
	h.sum += seconds
	for i, limit := range boundaries {
		if seconds <= limit {
			h.buckets[i]++
		}
	}
}

// Metrics 由 Service 创建并共享给所有 HTTP handler，使用短临界区维护真实数据。
// 只有固定 variant/operation/outcome 标签；订单号、request_id、trace_id 留在日志。
// 这里手写小型 Prometheus text exposition，目的是读清样本事实，不另搭运维平台。
type Metrics struct {
	mu       sync.Mutex
	variant  string
	inFlight int
	waiters  int
	counts   map[Outcome]uint64
	hists    map[string]*histogram
}

// NewMetrics 建立一次实验独立的指标对象；bad/fixed 是课程内部有限配置。
func NewMetrics(variant string) *Metrics {
	m := &Metrics{variant: variant, counts: make(map[Outcome]uint64), hists: make(map[string]*histogram)}
	for _, o := range outcomes {
		m.counts[o] = 0
	}
	for _, name := range []string{"request", "queue_wait", "dependency"} {
		m.hists[name] = &histogram{}
	}
	return m
}

// begin/finish 属于请求 handler 的配对操作。finish 在 handler 的退出 defer 内执行，
// 因而 panic、取消、业务拒绝也会把 in-flight 减回零，不只照顾正常 return。
func (m *Metrics) begin() { m.mu.Lock(); m.inFlight++; m.mu.Unlock() }
func (m *Metrics) finish(o Outcome, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight--
	m.counts[o]++
	m.hists["request"].observe(d)
}

// queueBegin/queueEnd 记录正在等待入场的数量；在本课程里既包括 Mutex.Lock，
// 也包括可取消的 select 入场门。Begin 表示尝试等待，goroutine 栈才能确认停在哪里。
func (m *Metrics) queueBegin() { m.mu.Lock(); m.waiters++; m.mu.Unlock() }
func (m *Metrics) queueEnd(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waiters--
	m.hists["queue_wait"].observe(d)
}

// dependency 在一次真实 HTTP RoundTrip 返回后记录，含等待响应时间；没有发出请求
// 的 canceled 不调用它，所以 dependency_count 没增加代表“没有样本”，不是耗时为零。
func (m *Metrics) dependency(d time.Duration) {
	m.mu.Lock()
	m.hists["dependency"].observe(d)
	m.mu.Unlock()
}

// Snapshot 复制业务计数，供实验独立断言。返回 map 是新对象，调用者不能改到共享状态。
func (m *Metrics) Snapshot() (map[Outcome]uint64, int, int, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copyCounts := make(map[Outcome]uint64)
	for k, v := range m.counts {
		copyCounts[k] = v
	}
	return copyCounts, m.inFlight, m.waiters, m.hists["dependency"].count
}

// WritePrometheus 被 /metrics 和证据保存调用。锁覆盖一次完整快照以避免看到半次更新，
// 它输出的是进程本次运行累计值；服务重启使 counter 重置，不能直接当成时间窗口。
func (m *Metrics) WritePrometheus(w io.Writer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	labels := fmt.Sprintf("variant=%q,operation=\"checkout\"", m.variant)
	// 已知失败与取消/结果未知分别计数。unknown 不能混入“已确认业务失败”口径。
	// 这些总数可由 outcome counter 推导，这里显式输出便于初学者看清口径。
	if _, err := fmt.Fprintf(w, "# TYPE m34_errors_total counter\nm34_errors_total{%s} %d\n# TYPE m34_cancellations_total counter\nm34_cancellations_total{%s} %d\n# TYPE m34_unknown_total counter\nm34_unknown_total{%s} %d\n", labels, m.counts[BusinessError]+m.counts[DependencyError]+m.counts[InternalError], labels, m.counts[Canceled], labels, m.counts[Unknown]); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# HELP m34_requests_total Completed checkout handlers classified by known business boundary.\n# TYPE m34_requests_total counter"); err != nil {
		return err
	}
	for _, o := range outcomes {
		if _, err := fmt.Fprintf(w, "m34_requests_total{%s,outcome=%q} %d\n", labels, o, m.counts[o]); err != nil {
			return err
		}
	}
	for _, g := range []struct {
		name string
		n    int
	}{{"in_flight", m.inFlight}, {"queue_waiters", m.waiters}} {
		if _, err := fmt.Fprintf(w, "# TYPE m34_%s gauge\nm34_%s{%s} %d\n", g.name, g.name, labels, g.n); err != nil {
			return err
		}
	}
	// map 迭代顺序不保证；排序使证据稳定可读，并不用于建立并发先后关系。
	names := make([]string, 0, len(m.hists))
	for name := range m.hists {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := m.hists[name]
		metric := "m34_" + name + "_seconds"
		if _, err := fmt.Fprintf(w, "# TYPE %s histogram\n", metric); err != nil {
			return err
		}
		for i, limit := range boundaries {
			if _, err := fmt.Fprintf(w, "%s_bucket{%s,le=%q} %d\n", metric, labels, fmt.Sprint(limit), h.buckets[i]); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s_bucket{%s,le=\"+Inf\"} %d\n%s_sum{%s} %.9f\n%s_count{%s} %d\n", metric, labels, h.count, metric, labels, h.sum, metric, labels, h.count); err != nil {
			return err
		}
	}
	return nil
}
