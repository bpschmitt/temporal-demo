# Temporal Demo

A small Go app that shows what [Temporal](https://temporal.io/) does well: durable execution, automatic retries, saga compensation, human-in-the-loop signals, queries, and durable timers. It's instrumented end to end with OpenTelemetry. Runs on Docker Compose or Kubernetes (kind).

```
curl ──► api (net/http + otelhttp) ──► Temporal server (dev server, Web UI :8233)
                                              ▲
                                  worker (OrderWorkflow, ExpenseApprovalWorkflow)

api, worker ──OTLP──► otel-collector ──► grafana/otel-lgtm (Grafana :3000: Tempo, Prometheus, Loki)
                          │          └─► New Relic (only if NEW_RELIC_LICENSE_KEY is set)
                          └─ scrapes Temporal server Prometheus metrics
```

## Workflows

**OrderWorkflow** ([internal/order](internal/order)) is a saga with three steps: `ReserveInventory` → `ChargePayment` → `ShipOrder`.
- Forward steps fail at random (30% by default), and Temporal retries them with exponential backoff.
- `fail_step` (`inventory` | `payment` | `shipping`) forces a permanent failure. Steps that already succeeded are then compensated in reverse order (`RefundPayment`, `ReleaseInventory`).
- `ShipOrder` runs for about 10s and records a heartbeat each second. Kill the worker partway through and another worker resumes from the last heartbeat.
- The `status` query returns the current step, completed steps and compensations.

**ExpenseApprovalWorkflow** ([internal/approval](internal/approval)) handles expense approval with a human in the loop.
- Expenses under 100 are approved automatically.
- Larger expenses wait for a `decision` signal (approve or reject) or a durable timer (`timeout_seconds`, default 120), whichever comes first. A workflow that is waiting uses no worker resources.
- The `status` query returns `PENDING_APPROVAL`, `APPROVED`, `REJECTED`, `TIMED_OUT` or `PAID`.

## Quick start: Docker Compose

```bash
make up                 # temporal, otel-collector, lgtm, worker, api
make demo-order         # prints workflow_id, e.g. order-1729...
```

| What | Where |
|---|---|
| API | http://localhost:8080 (change with `API_PORT=8090 make up`) |
| Temporal Web UI | http://localhost:8233 |
| Grafana | http://localhost:3000 |

Stop the stack with `make down`.

### Also send telemetry to New Relic

```bash
export NEW_RELIC_LICENSE_KEY=...   # ingest license key
# export NEW_RELIC_OTLP_ENDPOINT=https://otlp.eu01.nr-data.net   # EU accounts
make up
```

### Run the Go binaries on your host

`make up` publishes the collector on `localhost:4317`, so you can stop the containerized worker and api and run them with `go run` instead:

```bash
docker compose -f deploy/compose/docker-compose.yaml stop worker api
make run-worker      # terminal 1
make run-api         # terminal 2
```

You can also skip Docker entirely with `temporal server start-dev`. Telemetry export will log errors because no collector is running, but the workflows still work.

## Kubernetes (kind)

```bash
make kind-up         # creates kind cluster "temporal-demo", builds and loads the image, applies manifests
make port-forward    # api :8080, Temporal UI :8233, Grafana :3000
make kind-down
```

- Every kubectl call is pinned to `--context kind-temporal-demo`, so it won't touch your other clusters.
- The manifests live in [deploy/k8s/base](deploy/k8s/base) (Kustomize). The worker runs 2 replicas, so tasks are split across pods.
- Pod, namespace and node names are added to all telemetry through the Downward API (`OTEL_RESOURCE_ATTRIBUTES`).
- If `NEW_RELIC_LICENSE_KEY` is set, `make k8s-deploy` creates a `newrelic` Secret and applies [deploy/k8s/newrelic](deploy/k8s/newrelic).
- Temporal runs as the single-binary dev server. For a production-style install, use the [official Helm charts](https://github.com/temporalio/helm-charts).

## Demo script

Each `ID=` is any string you choose. On kind, run `make port-forward` first. Add `API_PORT=` if you changed the port.

1. **Retries.** Run `make demo-order ID=1`. In the Temporal UI, open `order-1`. Some activities show more than one attempt.
2. **Saga compensation.** Run `make demo-order-fail ID=2 FAIL=shipping`, then `make status ID=2`. The output shows `compensated: [RefundPayment, ReleaseInventory]`.
3. **Durability.** Run `make demo-order ID=3`. Within about 5s, kill the worker:
   - Compose: `docker compose -f deploy/compose/docker-compose.yaml kill worker`, then `... start worker`
   - kind: `kubectl --context kind-temporal-demo -n temporal-demo delete pod -l app=worker --wait=false`

   The order still completes. The worker log shows `resuming shipment from heartbeat`.
4. **Signals and queries.** Run `make demo-expense ID=4`. `make status TYPE=expenses ID=4` shows `PENDING_APPROVAL`. Then run `make approve ID=4` (or `make reject ID=4`) and check the status again.
5. **Durable timer.** Run `make demo-expense ID=5 TIMEOUT=10`. After 10s the status is `TIMED_OUT`.
6. **Auto-approval.** Run `make demo-expense-small ID=6`.

## Observability

| Signal | Source | What to look at |
|---|---|---|
| Traces | Temporal SDK tracing interceptor, `otelhttp`, custom child spans | One trace covers `POST /orders` → `StartWorkflow:OrderWorkflow` → `RunWorkflow` → `StartActivity`/`RunActivity:*` → `payment.charge` and similar spans, across the api and worker services. Retried attempts and attempts on other pods appear as sibling spans. |
| Metrics | Temporal SDK metrics handler, Go runtime, HTTP server, Temporal server (Prometheus scrape) | `temporal_activity_execution_latency`, `temporal_workflow_completed`, `temporal_workflow_task_schedule_to_start_latency`, the custom counters `orders_completed`, `orders_compensated` and `expense_decisions{decision}` (emitted from workflow code through Temporal's metrics handler, which doesn't double-count on replay), and the server's `service_requests` and `persistence_latency` |
| Logs | `slog` → `otelslog` bridge | Workflow and activity logs carry native `trace_id`/`span_id`, so you can jump between logs and traces in Grafana and New Relic. |

In Grafana, go to **Explore**:
- **Tempo:** search `{resource.service.name="temporal-demo-worker"}`, or paste a `trace_id` from the worker logs.
- **Prometheus:** query `orders_completed_total`.
- **Loki:** query `{service_name="temporal-demo-worker"}`.

In New Relic:

```sql
FROM Span SELECT count(*) FACET name WHERE service.name = 'temporal-demo-worker' SINCE 30 minutes ago
FROM Metric SELECT sum(orders_completed) TIMESERIES
FROM Log SELECT * WHERE service.name = 'temporal-demo-worker'
```

The Temporal server exports about 16k series per scrape, mostly histogram buckets. That's fine locally, but it counts toward New Relic ingest. To reduce it, raise `scrape_interval` or add `metric_relabel_configs` in [deploy/otel-collector/base.yaml](deploy/otel-collector/base.yaml).

## Layout

```
cmd/api, cmd/worker        binaries (one image, /app/api and /app/worker)
internal/order             saga workflow, activities, tests
internal/approval          signal/timer workflow, activities, tests
internal/api               REST handlers
internal/telemetry         OTel providers (traces, metrics, logs) + slog trace-context fix-up
internal/temporalx         Temporal client with OTel interceptor/metrics/logger
deploy/otel-collector      collector config: base.yaml + one overlay (local.yaml | newrelic.yaml)
deploy/compose             docker compose stack
deploy/k8s                 kind config + kustomize (base, newrelic)
```

## Configuration

| Env var | Default | Used by |
|---|---|---|
| `TEMPORAL_ADDRESS` | `localhost:7233` | api, worker |
| `TEMPORAL_NAMESPACE` | `default` | api, worker |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | api, worker |
| `OTEL_EXPORTER_OTLP_INSECURE` | `false` (`true` in compose/k8s) | api, worker |
| `OTEL_SERVICE_NAME` | `temporal-demo-api` / `temporal-demo-worker` | api, worker |
| `LISTEN_ADDR` | `:8080` | api |
| `TRANSIENT_FAILURE_RATE` | `0.3` | worker |
| `SHIP_SECONDS` | `10` | worker |
| `NEW_RELIC_LICENSE_KEY`, `NEW_RELIC_OTLP_ENDPOINT` | unset, `https://otlp.nr-data.net` | collector |

## Tests

```bash
make test   # go vet + workflow unit tests (Temporal test suite with time skipping)
```
