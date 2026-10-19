// Package approval implements a human-in-the-loop expense approval: small
// expenses are auto-approved, larger ones wait for a "decision" signal or
// time out on a durable timer.
package approval

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WorkflowName   = "ExpenseApprovalWorkflow"
	SignalDecision = "decision"
	QueryStatus    = "status"

	AutoApproveLimit = 100.0
	DefaultTimeout   = 2 * time.Minute

	StatusPending  = "PENDING_APPROVAL"
	StatusApproved = "APPROVED"
	StatusRejected = "REJECTED"
	StatusTimedOut = "TIMED_OUT"
	StatusPaid     = "PAID"
)

type Input struct {
	ExpenseID   string  `json:"expense_id"`
	Requester   string  `json:"requester"`
	Amount      float64 `json:"amount"`
	Description string  `json:"description"`
	// TimeoutSeconds overrides DefaultTimeout for the approval wait.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Decision is the payload of the "decision" signal.
type Decision struct {
	Approved bool   `json:"approved"`
	Approver string `json:"approver"`
	Comment  string `json:"comment"`
}

// Result is both the workflow result and the "status" query response.
type Result struct {
	ExpenseID string `json:"expense_id"`
	Status    string `json:"status"`
	Approver  string `json:"approver,omitempty"`
	Comment   string `json:"comment,omitempty"`
	PayoutID  string `json:"payout_id,omitempty"`
}

func Workflow(ctx workflow.Context, in Input) (Result, error) {
	logger := workflow.GetLogger(ctx)

	res := Result{ExpenseID: in.ExpenseID, Status: "SUBMITTED"}
	if err := workflow.SetQueryHandler(ctx, QueryStatus, func() (Result, error) {
		return res, nil
	}); err != nil {
		return res, err
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 5},
	})
	var a *Activities

	if in.Amount < AutoApproveLimit {
		res.Status, res.Approver, res.Comment = StatusApproved, "auto", "below auto-approve limit"
	} else {
		if err := workflow.ExecuteActivity(ctx, a.NotifyApprover, in).Get(ctx, nil); err != nil {
			return res, err
		}
		res.Status = StatusPending

		timeout := DefaultTimeout
		if in.TimeoutSeconds > 0 {
			timeout = time.Duration(in.TimeoutSeconds) * time.Second
		}

		// Wait for whichever comes first: a human decision or the timer.
		// While waiting the workflow consumes no worker resources at all.
		var decision Decision
		timedOut := false
		timerCtx, cancelTimer := workflow.WithCancel(ctx)
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(workflow.GetSignalChannel(ctx, SignalDecision), func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &decision)
			cancelTimer()
		})
		sel.AddFuture(workflow.NewTimer(timerCtx, timeout), func(f workflow.Future) {
			timedOut = f.Get(ctx, nil) == nil
		})
		sel.Select(ctx)

		switch {
		case timedOut:
			res.Status, res.Comment = StatusTimedOut, "no decision within "+timeout.String()
		case decision.Approved:
			res.Status, res.Approver, res.Comment = StatusApproved, decision.Approver, decision.Comment
		default:
			res.Status, res.Approver, res.Comment = StatusRejected, decision.Approver, decision.Comment
		}
	}

	workflow.GetMetricsHandler(ctx).
		WithTags(map[string]string{"decision": res.Status}).
		Counter("expense_decisions").Inc(1)
	logger.Info("expense decided", "expense_id", in.ExpenseID, "status", res.Status)

	if res.Status != StatusApproved {
		err := workflow.ExecuteActivity(ctx, a.NotifyRequester, in, res).Get(ctx, nil)
		return res, err
	}

	if err := workflow.ExecuteActivity(ctx, a.ProcessPayout, in).Get(ctx, &res.PayoutID); err != nil {
		return res, err
	}
	res.Status = StatusPaid
	return res, workflow.ExecuteActivity(ctx, a.NotifyRequester, in, res).Get(ctx, nil)
}
