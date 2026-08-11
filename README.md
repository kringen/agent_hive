# agent_hive
Highly Available Autonomous Agents Doing Tasks

A home-lab platform for running autonomous agents that pick up tasks from a
queue, use a cost-aware router to decide between a local LLM (Ollama) and a
cloud LLM (Claude), and route any risky/irreversible action through a human
approval queue before it executes.

## Architecture

```
            ┌──────────────┐        agent.tasks.>        ┌──────────────┐
  agents ──▶│     NATS     │─────────────────────────────▶│    router     │
 (Python/Go)│  JetStream   │◀─────────────────────────────│ (Go)          │
            │              │        agent.results.>       │ ollama/claude │
            └──────┬───────┘                              │ + budget.db   │
                   │ agent.budget.updates                  └──────────────┘
                   ▼
            ┌──────────────┐   WebSocket   ┌───────────────────┐
            │ dashboard-api │──────────────▶│ dashboard-frontend │
            │ (Go, approvals│               │ (static ops console)│
            │  queue + relay)│              └───────────────────┘
            └──────────────┘
```

- **`agent-platform/router`** — Consumes tasks from NATS JetStream
  (`agent.tasks.>`), classifies each task as cheap (Ollama) or high-quality
  (Claude) using a static routing table plus size/keyword heuristics,
  escalates low-confidence local answers to Claude (unless over budget),
  tracks spend in SQLite against a configurable monthly cap, and publishes
  results back over NATS.
- **`agent-platform/dashboard-api`** — SQLite-backed human approval queue for
  actions agents want to take, plus a WebSocket hub that relays live NATS
  task/result/budget events to the frontend.
- **`agent-platform/dashboard-frontend`** — Static ops console UI.
- **`agent-platform/agents`** — Example autonomous agents (starting with an
  email triage agent). Agents classify/act via the router, auto-execute
  low-risk reversible actions, and propose everything else for approval.

## Running locally

```bash
cd agent-platform
docker compose up --build
# Optional one-off agent run:
docker compose --profile agents run --rm email-agent
```

Dashboard: http://localhost:8080 · Dashboard API: http://localhost:8000 ·
NATS monitoring: http://localhost:8222/varz

NATS runs as a 3-node JetStream cluster (`nats-1`/`nats-2`/`nats-3`) for
high availability — see
[`agent-platform/docs/testing-ha.md`](agent-platform/docs/testing-ha.md)
for a step-by-step guide to verifying cluster formation and failover
(killing the leader node, confirming zero-downtime re-election, etc).

## Status / roadmap

This is an early-stage project. Known gaps being worked on:
- ~~NATS clustering~~ — done, see `agent-platform/docs/testing-ha.md`
- Multi-replica router/dashboard-api and replicated storage instead of
  local SQLite files
- ~~Explicit JetStream ack policy, redelivery backoff, and a dead-letter
  subject~~ — done
- ~~Health/readiness endpoints and container healthchecks~~ — done
  (`/healthz`, `/readyz` on both services; distroless-safe healthcheck
  binaries wired into Docker `HEALTHCHECK` + compose)
- ~~CI (build/vet/test) and committed `go.sum` files~~ — done
- ~~golangci-lint config~~ — done, see `.golangci.yml` / `make lint`
- Test coverage across router and dashboard-api
