package approval

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func newEnv() *testsuite.TestWorkflowEnvironment {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	return env
}

func result(t *testing.T, env *testsuite.TestWorkflowEnvironment) Result {
	t.Helper()
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var res Result
	require.NoError(t, env.GetWorkflowResult(&res))
	return res
}

func TestSmallExpenseAutoApproved(t *testing.T) {
	env := newEnv()
	env.ExecuteWorkflow(Workflow, Input{ExpenseID: "e1", Amount: 25})

	res := result(t, env)
	require.Equal(t, StatusPaid, res.Status)
	require.Equal(t, "auto", res.Approver)
	require.Equal(t, "payout-e1", res.PayoutID)
}

func TestLargeExpenseApprovedBySignal(t *testing.T) {
	env := newEnv()
	env.RegisterDelayedCallback(func() {
		v, err := env.QueryWorkflow(QueryStatus)
		require.NoError(t, err)
		var st Result
		require.NoError(t, v.Get(&st))
		require.Equal(t, StatusPending, st.Status)

		env.SignalWorkflow(SignalDecision, Decision{Approved: true, Approver: "alice", Comment: "ok"})
	}, time.Minute)

	env.ExecuteWorkflow(Workflow, Input{ExpenseID: "e2", Amount: 500})

	res := result(t, env)
	require.Equal(t, StatusPaid, res.Status)
	require.Equal(t, "alice", res.Approver)
}

func TestLargeExpenseRejectedBySignal(t *testing.T) {
	env := newEnv()
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalDecision, Decision{Approved: false, Approver: "bob", Comment: "no receipt"})
	}, time.Minute)

	env.ExecuteWorkflow(Workflow, Input{ExpenseID: "e3", Amount: 500})

	res := result(t, env)
	require.Equal(t, StatusRejected, res.Status)
	require.Empty(t, res.PayoutID)
}

func TestLargeExpenseTimesOut(t *testing.T) {
	env := newEnv()
	// No signal: the test environment skips time forward to fire the timer.
	env.ExecuteWorkflow(Workflow, Input{ExpenseID: "e4", Amount: 500, TimeoutSeconds: 30})

	res := result(t, env)
	require.Equal(t, StatusTimedOut, res.Status)
}
