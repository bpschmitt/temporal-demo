package order

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func newEnv(t *testing.T) (*testsuite.TestWorkflowEnvironment, *Activities) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	return env, a
}

func TestOrderHappyPath(t *testing.T) {
	env, _ := newEnv(t)

	env.ExecuteWorkflow(Workflow, Input{OrderID: "1", Amount: 42})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var res Result
	require.NoError(t, env.GetWorkflowResult(&res))
	require.Equal(t, Result{OrderID: "1", ReservationID: "rsv-1", PaymentID: "pay-1", TrackingID: "trk-1"}, res)

	v, err := env.QueryWorkflow(QueryStatus)
	require.NoError(t, err)
	var st Status
	require.NoError(t, v.Get(&st))
	require.Equal(t, "COMPLETED", st.Step)
	require.Equal(t, []string{"ReserveInventory", "ChargePayment", "ShipOrder"}, st.Completed)
}

func TestOrderTransientFailureIsRetried(t *testing.T) {
	env, a := newEnv(t)
	env.OnActivity(a.ChargePayment, mock.Anything, mock.Anything).
		Return("", errors.New("payment service unavailable")).Twice()
	env.OnActivity(a.ChargePayment, mock.Anything, mock.Anything).Return("pay-1", nil).Once()

	env.ExecuteWorkflow(Workflow, Input{OrderID: "1"})

	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestOrderShippingFailureCompensatesInReverse(t *testing.T) {
	env, a := newEnv(t)
	var calls []string
	env.OnActivity(a.RefundPayment, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { calls = append(calls, "RefundPayment") }).Return(nil).Once()
	env.OnActivity(a.ReleaseInventory, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { calls = append(calls, "ReleaseInventory") }).Return(nil).Once()

	env.ExecuteWorkflow(Workflow, Input{OrderID: "1", FailStep: "shipping"})

	err := env.GetWorkflowError()
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, ErrShippingUnavailable, appErr.Type())
	require.Equal(t, []string{"RefundPayment", "ReleaseInventory"}, calls)
	env.AssertExpectations(t)
}

func TestOrderPaymentDeclinedOnlyReleasesInventory(t *testing.T) {
	env, a := newEnv(t)
	env.OnActivity(a.ReleaseInventory, mock.Anything, mock.Anything).Return(nil).Once()
	// Mocked so calls are recorded and AssertNotCalled is meaningful.
	env.OnActivity(a.RefundPayment, mock.Anything, mock.Anything).Return(nil).Maybe()

	env.ExecuteWorkflow(Workflow, Input{OrderID: "1", FailStep: "payment"})

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, env.GetWorkflowError(), &appErr)
	require.Equal(t, ErrPaymentDeclined, appErr.Type())
	env.AssertExpectations(t)
	env.AssertNotCalled(t, "RefundPayment", mock.Anything, mock.Anything)
}
