package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

type captureHandler struct {
	ctx   context.Context
	attrs map[string]any
	pre   []slog.Attr
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureHandler) WithGroup(string) slog.Handler           { return c }
func (c *captureHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c.pre = append(c.pre, as...)
	return c
}
func (c *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	c.ctx, c.attrs = ctx, map[string]any{}
	for _, a := range c.pre {
		c.attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool { c.attrs[a.Key] = a.Value.Any(); return true })
	return nil
}

func TestTraceContextHandler(t *testing.T) {
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")

	cap := &captureHandler{}
	// Mirrors how the Temporal tracing interceptor decorates its logger.
	slog.New(&traceContextHandler{next: cap}).With("TraceID", tid, "SpanID", sid).Info("hello", "k", "v")

	require.Equal(t, tid.String(), cap.attrs["trace_id"])
	require.Equal(t, sid.String(), cap.attrs["span_id"])
	require.Equal(t, "v", cap.attrs["k"])
	sc := trace.SpanContextFromContext(cap.ctx)
	require.Equal(t, tid, sc.TraceID())
	require.Equal(t, sid, sc.SpanID())
}
