// Package temporalx builds a Temporal client with OpenTelemetry tracing,
// metrics and logging wired in.
package temporalx

import (
	"log/slog"
	"os"

	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	tlog "go.temporal.io/sdk/log"
)

// TaskQueue is the single task queue used by every workflow in the demo.
const TaskQueue = "temporal-demo"

// Dial connects to the Temporal frontend named by TEMPORAL_ADDRESS
// (default localhost:7233) in TEMPORAL_NAMESPACE (default "default").
//
// The tracing interceptor is registered on the client; workers created from
// this client inherit it, so a single trace follows a request from the HTTP
// API through StartWorkflow, RunWorkflow and every activity.
func Dial(logger *slog.Logger) (client.Client, error) {
	tracing, err := temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{})
	if err != nil {
		return nil, err
	}

	return client.Dial(client.Options{
		HostPort:     envOr("TEMPORAL_ADDRESS", client.DefaultHostPort),
		Namespace:    envOr("TEMPORAL_NAMESPACE", client.DefaultNamespace),
		Interceptors: []interceptor.ClientInterceptor{tracing},
		MetricsHandler: temporalotel.NewMetricsHandler(temporalotel.MetricsHandlerOptions{
			OnError: func(err error) { logger.Error("temporal metrics error", "error", err) },
			// Export counters as monotonic sums (e.g. orders_completed_total)
			// rather than up/down counters.
			UseMonotonicCounters: true,
		}),
		Logger: tlog.NewStructuredLogger(logger),
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
