package lab

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// spanRecord 是结束 span 的独立证据，用来核对跨真实 HTTP 的父子关系。
// 这不是 exporter 替身：captureProcessor 旁路复制元数据，同时真实 stdouttrace
// exporter 把完整 span 写进本地文件。只有后者成功落盘，才声称“本地导出”。
type spanRecord struct {
	Name         string   `json:"name"`
	TraceID      string   `json:"trace_id"`
	SpanID       string   `json:"span_id"`
	Parent       string   `json:"parent_span_id"`
	RemoteParent bool     `json:"remote_parent"`
	Links        []string `json:"links,omitempty"`
}

type captureProcessor struct {
	mu      sync.Mutex
	records []spanRecord
}

func (*captureProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

// OnEnd 由 SDK 在 span.End 时调用，复制 ID 而不保留 HTTP request/大对象。
// 不把调用者的 slice 直接返回出去；否则后续 append 与读者可能争用同一底层数组。
func (p *captureProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	r := spanRecord{Name: s.Name(), TraceID: s.SpanContext().TraceID().String(), SpanID: s.SpanContext().SpanID().String(), Parent: s.Parent().SpanID().String(), RemoteParent: s.Parent().IsRemote()}
	for _, link := range s.Links() {
		r.Links = append(r.Links, link.SpanContext.TraceID().String()+"/"+link.SpanContext.SpanID().String())
	}
	p.mu.Lock()
	p.records = append(p.records, r)
	p.mu.Unlock()
}
func (*captureProcessor) Shutdown(context.Context) error   { return nil }
func (*captureProcessor) ForceFlush(context.Context) error { return nil }
func (p *captureProcessor) snapshot() []spanRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]spanRecord(nil), p.records...)
}

// Telemetry 属于本次 Run，拥有 SDK provider 的 Shutdown。
// logger 的 JSONHandler 内部同步单条 Write；多个 handler 共用它不会拼出半条 JSON。
// slog.InfoContext 不会自动提取 trace ID，所以业务代码显式 With(trace_id)。
type Telemetry struct {
	Logger     *slog.Logger
	Tracer     trace.Tracer
	Propagator propagation.TextMapPropagator
	Provider   *sdktrace.TracerProvider
	capture    *captureProcessor
}

// NewTelemetry 由入口传入两个独立 writer：日志与完整 OTel span 各写一个文件。
// AlwaysSample 只为有界课程看清每条链路，生产采样/丢弃可能使“没有 span”不等于没执行。
// WithSyncer 使 Span.End 同步写 exporter，便于演示落盘；其开销也计入本次实验环境。
func NewTelemetry(logWriter, spanWriter io.Writer) (*Telemetry, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithWriter(spanWriter))
	if err != nil {
		return nil, err
	}
	capture := &captureProcessor{}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "m34-local-checkout"))),
		sdktrace.WithSpanProcessor(capture), sdktrace.WithSyncer(exporter),
	)
	return &Telemetry{Logger: slog.New(slog.NewJSONHandler(logWriter, &slog.HandlerOptions{AddSource: true})), Tracer: provider.Tracer("go-demo/m34"), Propagator: propagation.TraceContext{}, Provider: provider, capture: capture}, nil
}
