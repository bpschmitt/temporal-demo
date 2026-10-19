package order

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

var tracer = otel.Tracer("github.com/bpschmitt/temporal-demo/internal/order")

// Activities simulate calls to inventory, payment and shipping services.
type Activities struct {
	// TransientFailureRate (0..1) makes forward steps fail randomly so
	// Temporal's automatic retries are visible in the UI and in traces.
	TransientFailureRate float64
	// ShipDuration is how long ShipOrder runs, heartbeating every second.
	ShipDuration time.Duration
}

func (a *Activities) ReserveInventory(ctx context.Context, in Input) (string, error) {
	if in.FailStep == "inventory" {
		return "", temporal.NewApplicationError("items out of stock", ErrOutOfStock)
	}
	if err := a.flaky(ctx, "inventory"); err != nil {
		return "", err
	}
	downstream(ctx, "inventory.reserve", in)
	return "rsv-" + in.OrderID, nil
}

func (a *Activities) ChargePayment(ctx context.Context, in Input) (string, error) {
	if in.FailStep == "payment" {
		return "", temporal.NewApplicationError("card declined", ErrPaymentDeclined)
	}
	if err := a.flaky(ctx, "payment"); err != nil {
		return "", err
	}
	downstream(ctx, "payment.charge", in)
	return "pay-" + in.OrderID, nil
}

func (a *Activities) ShipOrder(ctx context.Context, in Input) (string, error) {
	logger := activity.GetLogger(ctx)

	// Resume from the last heartbeat if a previous attempt died mid-flight.
	start := 0
	if activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &start); err == nil {
			logger.Info("resuming shipment from heartbeat", "order_id", in.OrderID, "second", start)
		}
	}

	ticks := int(a.ShipDuration / time.Second)
	for i := start; i < ticks; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
		activity.RecordHeartbeat(ctx, i+1)
	}

	if in.FailStep == "shipping" {
		return "", temporal.NewApplicationError("no carrier available", ErrShippingUnavailable)
	}
	downstream(ctx, "shipping.create_label", in)
	return "trk-" + in.OrderID, nil
}

func (a *Activities) ReleaseInventory(ctx context.Context, in Input) error {
	activity.GetLogger(ctx).Info("releasing inventory", "order_id", in.OrderID)
	downstream(ctx, "inventory.release", in)
	return nil
}

func (a *Activities) RefundPayment(ctx context.Context, in Input) error {
	activity.GetLogger(ctx).Info("refunding payment", "order_id", in.OrderID, "amount", in.Amount)
	downstream(ctx, "payment.refund", in)
	return nil
}

func (a *Activities) flaky(ctx context.Context, step string) error {
	if rand.Float64() < a.TransientFailureRate {
		return fmt.Errorf("%s service unavailable (simulated, attempt %d)", step, activity.GetInfo(ctx).Attempt)
	}
	return nil
}

// downstream emits a child span under the activity span, standing in for a
// real call to another service.
func downstream(ctx context.Context, name string, in Input) {
	_, span := tracer.Start(ctx, name)
	defer span.End()
	span.SetAttributes(
		attribute.String("order.id", in.OrderID),
		attribute.Float64("order.amount", in.Amount),
	)
	time.Sleep(time.Duration(50+rand.IntN(250)) * time.Millisecond)
}
