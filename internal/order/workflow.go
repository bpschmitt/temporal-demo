// Package order implements an order-fulfillment saga: reserve inventory,
// charge payment, ship. If any step fails permanently, the steps that already
// succeeded are compensated in reverse order.
package order

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WorkflowName = "OrderWorkflow"
	QueryStatus  = "status"

	ErrOutOfStock          = "OutOfStock"
	ErrPaymentDeclined     = "PaymentDeclined"
	ErrShippingUnavailable = "ShippingUnavailable"
)

type Input struct {
	OrderID string   `json:"order_id"`
	Items   []string `json:"items"`
	Amount  float64  `json:"amount"`
	// FailStep forces a permanent failure at "inventory", "payment" or
	// "shipping" to demonstrate compensation.
	FailStep string `json:"fail_step,omitempty"`
}

type Result struct {
	OrderID       string `json:"order_id"`
	ReservationID string `json:"reservation_id"`
	PaymentID     string `json:"payment_id"`
	TrackingID    string `json:"tracking_id"`
}

// Status is exposed through the "status" query.
type Status struct {
	Step        string   `json:"step"`
	Completed   []string `json:"completed"`
	Compensated []string `json:"compensated"`
	Error       string   `json:"error,omitempty"`
}

type compensation struct {
	name string
	fn   any
}

func Workflow(ctx workflow.Context, in Input) (Result, error) {
	logger := workflow.GetLogger(ctx)
	metrics := workflow.GetMetricsHandler(ctx)

	status := Status{Step: "STARTED", Completed: []string{}, Compensated: []string{}}
	if err := workflow.SetQueryHandler(ctx, QueryStatus, func() (Status, error) {
		return status, nil
	}); err != nil {
		return Result{}, err
	}

	retry := &temporal.RetryPolicy{
		InitialInterval:        time.Second,
		BackoffCoefficient:     2.0,
		MaximumInterval:        10 * time.Second,
		MaximumAttempts:        5,
		NonRetryableErrorTypes: []string{ErrOutOfStock, ErrPaymentDeclined, ErrShippingUnavailable},
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy:         retry,
	})
	// Shipping is slow and heartbeats, so a crashed worker is detected
	// quickly and the activity resumes from its last heartbeat elsewhere.
	shipCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		HeartbeatTimeout:    5 * time.Second,
		RetryPolicy:         retry,
	})

	var (
		a      *Activities
		result = Result{OrderID: in.OrderID}
		comps  []compensation
	)

	compensate := func(cause error) (Result, error) {
		status.Step = "COMPENSATING"
		status.Error = cause.Error()
		logger.Warn("order failed, compensating", "order_id", in.OrderID, "error", cause)

		// A disconnected context lets compensations run even if the
		// workflow itself was cancelled.
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		for i := len(comps) - 1; i >= 0; i-- {
			if err := workflow.ExecuteActivity(dctx, comps[i].fn, in).Get(dctx, nil); err != nil {
				logger.Error("compensation failed", "step", comps[i].name, "error", err)
				continue
			}
			status.Compensated = append(status.Compensated, comps[i].name)
		}
		status.Step = "FAILED"
		metrics.Counter("orders_compensated").Inc(1)
		return result, cause
	}

	status.Step = "RESERVING_INVENTORY"
	if err := workflow.ExecuteActivity(ctx, a.ReserveInventory, in).Get(ctx, &result.ReservationID); err != nil {
		return compensate(err)
	}
	comps = append(comps, compensation{"ReleaseInventory", a.ReleaseInventory})
	status.Completed = append(status.Completed, "ReserveInventory")

	status.Step = "CHARGING_PAYMENT"
	if err := workflow.ExecuteActivity(ctx, a.ChargePayment, in).Get(ctx, &result.PaymentID); err != nil {
		return compensate(err)
	}
	comps = append(comps, compensation{"RefundPayment", a.RefundPayment})
	status.Completed = append(status.Completed, "ChargePayment")

	status.Step = "SHIPPING"
	if err := workflow.ExecuteActivity(shipCtx, a.ShipOrder, in).Get(ctx, &result.TrackingID); err != nil {
		return compensate(err)
	}
	status.Completed = append(status.Completed, "ShipOrder")

	status.Step = "COMPLETED"
	// Workflow metrics handlers are replay-aware: replays do not double count.
	metrics.Counter("orders_completed").Inc(1)
	logger.Info("order completed", "order_id", in.OrderID, "tracking_id", result.TrackingID)
	return result, nil
}
