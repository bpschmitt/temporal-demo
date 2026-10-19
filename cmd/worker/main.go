// Command worker hosts the order and expense-approval workflows and their
// activities.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/bpschmitt/temporal-demo/internal/approval"
	"github.com/bpschmitt/temporal-demo/internal/order"
	"github.com/bpschmitt/temporal-demo/internal/telemetry"
	"github.com/bpschmitt/temporal-demo/internal/temporalx"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, logger, err := telemetry.Setup(ctx, "temporal-demo-worker")
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}()

	c, err := temporalx.Dial(logger)
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, temporalx.TaskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(order.Workflow, workflow.RegisterOptions{Name: order.WorkflowName})
	w.RegisterWorkflowWithOptions(approval.Workflow, workflow.RegisterOptions{Name: approval.WorkflowName})
	w.RegisterActivity(&order.Activities{
		TransientFailureRate: envFloat("TRANSIENT_FAILURE_RATE", 0.3),
		ShipDuration:         time.Duration(envFloat("SHIP_SECONDS", 10)) * time.Second,
	})
	w.RegisterActivity(&approval.Activities{})

	if err := w.Start(); err != nil {
		return err
	}
	logger.Info("worker started", "task_queue", temporalx.TaskQueue)
	<-ctx.Done()
	w.Stop()
	return nil
}

func envFloat(key string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return v
	}
	return fallback
}
