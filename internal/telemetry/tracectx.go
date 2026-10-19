package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// traceContextHandler fixes up logs from the Temporal tracing interceptor.
//
// Workflow and activity loggers carry the current span as "TraceID"/"SpanID"
// attributes (raw byte arrays) rather than via context.Context. This handler
// turns them into hex "trace_id"/"span_id" attributes and puts the span
// context on the record's context, so the OTLP log bridge emits real
// trace/span IDs and backends can link logs to traces.
type traceContextHandler struct {
	next slog.Handler
	sc   trace.SpanContext
}

func (h *traceContextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *traceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := h.sc
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(fixTraceAttr(a, &sc))
		return true
	})
	if sc.IsValid() && !trace.SpanContextFromContext(ctx).IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, sc)
	}
	return h.next.Handle(ctx, out)
}

func (h *traceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	sc := h.sc
	fixed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		fixed[i] = fixTraceAttr(a, &sc)
	}
	return &traceContextHandler{next: h.next.WithAttrs(fixed), sc: sc}
}

func (h *traceContextHandler) WithGroup(name string) slog.Handler {
	return &traceContextHandler{next: h.next.WithGroup(name), sc: h.sc}
}

func fixTraceAttr(a slog.Attr, sc *trace.SpanContext) slog.Attr {
	switch v := a.Value.Any().(type) {
	case trace.TraceID:
		*sc = sc.WithTraceID(v).WithTraceFlags(trace.FlagsSampled)
		return slog.String("trace_id", v.String())
	case trace.SpanID:
		*sc = sc.WithSpanID(v).WithTraceFlags(trace.FlagsSampled)
		return slog.String("span_id", v.String())
	}
	return a
}
