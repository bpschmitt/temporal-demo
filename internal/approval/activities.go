package approval

import (
	"context"
	"math/rand/v2"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.temporal.io/sdk/activity"
)

var tracer = otel.Tracer("github.com/bpschmitt/temporal-demo/internal/approval")

// Activities simulate notification and payout services.
type Activities struct{}

func (a *Activities) NotifyApprover(ctx context.Context, in Input) error {
	activity.GetLogger(ctx).Info("approval requested",
		"expense_id", in.ExpenseID, "requester", in.Requester, "amount", in.Amount)
	downstream(ctx, "notify.approver", in)
	return nil
}

func (a *Activities) NotifyRequester(ctx context.Context, in Input, res Result) error {
	activity.GetLogger(ctx).Info("notifying requester",
		"expense_id", in.ExpenseID, "requester", in.Requester, "status", res.Status)
	downstream(ctx, "notify.requester", in)
	return nil
}

func (a *Activities) ProcessPayout(ctx context.Context, in Input) (string, error) {
	downstream(ctx, "payments.payout", in)
	return "payout-" + in.ExpenseID, nil
}

// downstream emits a child span under the activity span, standing in for a
// real call to another service.
func downstream(ctx context.Context, name string, in Input) {
	_, span := tracer.Start(ctx, name)
	defer span.End()
	span.SetAttributes(
		attribute.String("expense.id", in.ExpenseID),
		attribute.Float64("expense.amount", in.Amount),
	)
	time.Sleep(time.Duration(50+rand.IntN(150)) * time.Millisecond)
}
