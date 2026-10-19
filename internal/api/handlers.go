// Package api exposes a small REST API for starting, signalling and querying
// the demo workflows.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/bpschmitt/temporal-demo/internal/approval"
	"github.com/bpschmitt/temporal-demo/internal/order"
	"github.com/bpschmitt/temporal-demo/internal/temporalx"
)

type Server struct {
	temporal client.Client
	logger   *slog.Logger
}

func NewServer(c client.Client, logger *slog.Logger) *Server {
	return &Server{temporal: c, logger: logger}
}

// Handler returns the routes, each wrapped in its own otelhttp handler so the
// span is named after the route pattern.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, otelhttp.NewHandler(h, pattern))
	}

	handle("POST /orders", s.startOrder)
	handle("GET /orders/{id}", s.describe("order-", order.QueryStatus))
	handle("POST /expenses", s.startExpense)
	handle("GET /expenses/{id}", s.describe("expense-", approval.QueryStatus))
	handle("POST /expenses/{id}/approve", s.decide(true))
	handle("POST /expenses/{id}/reject", s.decide(false))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (s *Server) startOrder(w http.ResponseWriter, r *http.Request) {
	var in order.Input
	if !s.decode(w, r, &in) {
		return
	}
	if in.OrderID == "" {
		in.OrderID = newID()
	}
	s.start(w, r, "order-"+in.OrderID, order.WorkflowName, in)
}

func (s *Server) startExpense(w http.ResponseWriter, r *http.Request) {
	var in approval.Input
	if !s.decode(w, r, &in) {
		return
	}
	if in.ExpenseID == "" {
		in.ExpenseID = newID()
	}
	s.start(w, r, "expense-"+in.ExpenseID, approval.WorkflowName, in)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request, id, workflowName string, in any) {
	run, err := s.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:        id,
		TaskQueue: temporalx.TaskQueue,
	}, workflowName, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger.InfoContext(r.Context(), "workflow started", "workflow_id", run.GetID(), "run_id", run.GetRunID())
	writeJSON(w, http.StatusAccepted, map[string]string{
		"workflow_id": run.GetID(),
		"run_id":      run.GetRunID(),
	})
}

// describe reports the workflow's execution status plus its "status" query.
func (s *Server) describe(prefix, query string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := prefix + r.PathValue("id")

		desc, err := s.temporal.DescribeWorkflowExecution(r.Context(), id, "")
		if err != nil {
			s.fail(w, err)
			return
		}
		resp := map[string]any{
			"workflow_id":     id,
			"workflow_status": desc.GetWorkflowExecutionInfo().GetStatus().String(),
		}

		if v, err := s.temporal.QueryWorkflow(r.Context(), id, "", query); err != nil {
			resp["query_error"] = err.Error()
		} else {
			var state any
			if err := v.Get(&state); err == nil {
				resp["state"] = state
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *Server) decide(approved bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := approval.Decision{Approver: "anonymous"}
		if r.ContentLength > 0 && !s.decode(w, r, &d) {
			return
		}
		d.Approved = approved

		id := "expense-" + r.PathValue("id")
		if err := s.temporal.SignalWorkflow(r.Context(), id, "", approval.SignalDecision, d); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"workflow_id": id, "decision": d})
	}
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var notFound *serviceerror.NotFound
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	switch {
	case errors.As(err, &notFound):
		status = http.StatusNotFound
	case errors.As(err, &started):
		status = http.StatusConflict
	default:
		s.logger.Error("temporal call failed", "error", err)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
