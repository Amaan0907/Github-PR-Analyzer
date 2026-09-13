# Architecture Decisions — GitHub PR Risk Analyzer

**Derived from:** [HLD.md](./HLD.md) · **Source of truth:** [`../pr-risk-analyzer-plan.md`](../pr-risk-analyzer-plan.md)
**Related:** [LLD.md](./LLD.md) · [database-design.md](./database-design.md) · [development-plan.md](./development-plan.md)

Only decisions with real, non-obvious trade-offs are recorded here — per the task's instruction not to create ADRs for trivial decisions. Each is directly attributable to a specific statement in `plan.md`, not invented.

---

## ADR-001: PostgreSQL over MongoDB

### Context
The system needs to store structured review results, findings, and cost data, and later support dashboard analytics (joins, aggregates, time-bucketed queries) as well as strict idempotency guarantees.

### Decision
Use PostgreSQL as the sole system of record, with `JSONB` columns for genuinely schemaless data (`signals`, `category_scores`, `stats`, `settings`, `config`).

### Alternatives
- MongoDB, for "flexibility" on the findings/signals shape.
- A hybrid: Postgres for relational data, a separate document store for findings.

### Reasoning
`plan.md` §1 states this directly: *"Pick Postgres, not Mongo. Findings and analytics need joins, aggregates, and constraints. `JSONB` covers the schemaless parts."* Idempotency (a partial unique index) and guarded state transitions (conditional `UPDATE`) are relational-database-native guarantees that a document store would require re-implementing, with weaker consistency, in application code. A second datastore for findings would add operational surface area without solving a problem Postgres/JSONB doesn't already solve.

### Trade-offs
**Gained:** native joins for analytics, strong idempotency/constraint guarantees, one datastore to operate. **Lost:** JSONB columns are less rigidly typed than fully normalized tables — schema drift inside those columns is caught at the application layer (via `scoring_version`/validation), not by the database.

### Consequences
All analytics queries (`plan.md` §4 `/analytics/*`) are plain SQL, not an application-side aggregation layer. `signals`/`category_scores` evolution never requires a migration, but does require the reading code to handle multiple historical shapes (mitigated by `scoring_version`). See [database-design.md](./database-design.md#1-database-choice-and-reasoning).

---

## ADR-002: GitHub App over OAuth App or Personal Access Token

### Context
The system needs to authenticate to GitHub both to fetch PR data and to post results, across many installed repositories, at higher rate limits than a personal account would allow.

### Decision
Use a GitHub App exclusively, with per-installation access tokens. No OAuth App, no PAT, in any environment.

### Alternatives
- A Personal Access Token for simplicity, especially in early development.
- An OAuth App with a bot account.

### Reasoning
`plan.md` §5 and §10 are explicit: GitHub Apps provide per-installation tokens, fine-grained permissions, higher rate limits, and Checks API access that OAuth Apps/PATs don't cleanly offer. `plan.md` §17 names "using a PAT in development" as a concrete anti-pattern: *"The token has different scopes and rate limits than an installation token, so everything breaks at deployment."*

### Trade-offs
**Gained:** correct rate limits and permission scoping from day one, no dev/prod auth divergence, Checks API access. **Lost:** more upfront setup complexity (App registration, JWT minting, installation-token caching) versus a PAT's near-zero setup.

### Consequences
A test GitHub App must be registered against a test org before Phase 1 can begin (`plan.md` §13 Phase 1). The `github.App` component (JWT mint + Redis-cached installation tokens) is mandatory infrastructure, not an optional convenience layer.

---

## ADR-003: Two Services (API, Worker), Not Microservices

### Context
Webhook handling has a hard 10-second GitHub timeout; PR analysis takes up to ~60 seconds and involves expensive external calls. These two workloads have different latency profiles and failure blast radii.

### Decision
Split into exactly two deployable binaries — `cmd/api` and `cmd/worker` — from a single Go module and shared internal packages. Do not decompose further into per-domain microservices.

### Alternatives
- A single monolithic binary handling both webhook receipt and analysis inline.
- A full microservices decomposition (separate services per pipeline stage: diff service, LLM service, scoring service, publisher service).

### Reasoning
`plan.md` §1 states the constraint directly (rule 1: the webhook handler does no analysis) and the structure directly (rule 2: "The API service and the worker service are separate deployables sharing one codebase... They scale independently"). Nothing in `plan.md` describes independent domain ownership, separate data stores per component, or organizational boundaries that would justify a further microservices split — the stated reason for the API/worker split is purely latency/scaling, which two services already satisfy.

### Trade-offs
**Gained:** independent scaling of cheap-bursty (API) vs. expensive-steady (worker) workloads, without the operational overhead of N independently deployed services, N sets of inter-service contracts, and N failure domains. **Lost:** a true microservices system's per-component independent deployability and language flexibility — not a real loss here, since nothing in the requirements calls for either.

### Consequences
All pipeline-stage packages (`diff`, `signals`, `llm`, `findings`, `scoring`, `publisher`) are Go packages within the worker binary, not network-separated services — their interfaces (see [LLD.md §2](./LLD.md#2-core-interfaces)) are Go interfaces, not RPC/HTTP contracts.

---

## ADR-004: Redis + `asynq` for the Queue, Not Raw Redis or a Message Broker

### Context
Webhook-triggered work must be handed off asynchronously, with retries, backoff, and per-commit uniqueness guarantees, from the API service to the worker service.

### Decision
Use Redis with the `asynq` library.

### Alternatives
- Raw Redis lists/streams with hand-rolled retry/backoff/uniqueness logic.
- A dedicated message broker (RabbitMQ, Kafka, SQS).

### Reasoning
`plan.md` §1 states: *"Redis + `asynq` — Retries, backoff, scheduling, unique-task locks, dead-letter, and a web UI you get for free."* A dedicated broker like Kafka is built for durable event streaming at a scale and with ordering guarantees this system does not need; it would add substantial operational complexity (its own cluster, consumer group semantics) to solve a task-queue problem, not a streaming problem. Hand-rolling retry/backoff/uniqueness on raw Redis reproduces exactly what `asynq` already provides, which `plan.md` §16 item 4 flags as a mistake to avoid by building idempotency early rather than bolting it on.

### Trade-offs
**Gained:** unique-task locks (the queue-level idempotency guard), exponential backoff, priority queues, and a free inspection UI (`asynqmon`), all without custom code. **Lost:** Redis-backed queues are less durable than a log-structured broker under extreme failure (a Redis data-loss event could drop in-flight tasks) — acceptable here because Redis is explicitly documented as transient state, with the database's partial unique index as a second, independent idempotency guard (see [ADR-009, below](#adr-009-dual-guard-idempotency-queue-unique-key--database-partial-unique-index)).

### Consequences
Redis holds no data that must survive a Redis failure without consequence beyond "some in-flight reviews need to be re-triggered" — this is why `plan.md` §1 explicitly separates Redis (transient) from Postgres (system of record).

---

## ADR-005: Deterministic Scoring, Never an LLM-Assigned Score

### Context
The system must produce a 0–100 risk score. An LLM could plausibly be asked to output that number directly, the way many "AI code review" tools do.

### Decision
The LLM only emits **findings** (category, severity, evidence, confidence). A separate, pure, deterministic function (`internal/scoring`) computes the score from findings plus independently-computed signals. This is stated as a **non-negotiable architectural rule**, not a preference (`plan.md` §1 rule 4).

### Alternatives
- Ask the LLM for a 0–100 score directly, optionally alongside its findings.
- Have the LLM propose a score that a deterministic function only adjusts.

### Reasoning
`plan.md` §7: *"Rule: the model finds problems; code assigns numbers. A reviewer can dispute a finding. They cannot dispute arithmetic they can read."* §19 item 1 elevates this to the project's single most important talking point: reproducibility, testability, and defensibility that an LLM-generated number structurally cannot offer, because LLM output is not guaranteed reproducible even at temperature 0 across provider updates.

### Trade-offs
**Gained:** full reproducibility (any historical score recomputable offline), full explainability (a `Contribution` breakdown), full testability (golden files, property tests, 1000-run determinism checks) — none of which are achievable if the score itself comes from the model. **Lost:** the deterministic formula requires manual weight/constant tuning (the `K_c` saturation constants, category weights) via an evaluation harness, rather than "letting the model figure out severity holistically" — a real engineering cost, accepted deliberately.

### Consequences
`internal/scoring` must import nothing beyond the Go standard library (enforced structurally, not just by convention) — see [ADR-007, below](#adr-007-scoring-package-restricted-to-standard-library-only-imports). Every review stores `scoring_version` specifically so this determinism claim remains falsifiable/auditable over time.

---

## ADR-006: `analyze` and `publish` as Separate Queue Tasks

### Context
After analysis completes, results must be posted to GitHub (check run, comments). Posting can fail independently of analysis (e.g., a transient `422` or GitHub outage).

### Decision
`review:analyze` and `review:publish` are distinct task types, the second enqueued by the first only after analysis fully completes and persists.

### Alternatives
- A single task that analyzes and publishes in one handler, retried as a unit on any failure.

### Reasoning
`plan.md` §8: *"Split `analyze` and `publish` into separate tasks. Analysis is expensive and must never be re-run because a comment POST failed."* §16 item 8 reinforces this as a "build it this way from the start" constraint: *"Merging them later is easy; splitting them after retry logic exists is not."*

### Trade-offs
**Gained:** a flaky GitHub comment POST retries cheaply (2-minute timeout, 5 retries) without re-spending LLM cost; each task has an appropriately different timeout/retry budget. **Lost:** slightly more orchestration complexity (two task types, a hand-off between them) versus one combined handler.

### Consequences
The publish task must independently re-verify the review hasn't been superseded since analysis completed (`plan.md` §8) — a check that wouldn't be necessary if publish were inline with analyze.

---

## ADR-007: `scoring` Package Restricted to Standard-Library-Only Imports

### Context
Scoring must be reproducible and unit-testable in isolation from the rest of the system.

### Decision
`internal/scoring` imports nothing beyond the Go standard library — no database driver, no HTTP client, no third-party dependency of any kind.

### Alternatives
- Allow `scoring` to query the database for "just one lookup" (e.g., a repo's historical percentile) when convenient.

### Reasoning
`plan.md` §7: *"Package `internal/scoring` imports nothing but stdlib."* §16 item 3 explains why this is enforced as a constraint rather than a guideline: *"Once scoring makes a DB call for 'just one lookup,' it becomes untestable and non-reproducible, and you'll never get it back."* This is presented as a one-way door.

### Trade-offs
**Gained:** scoring tests run with zero I/O, zero mocking, zero flakiness; the function is trivially reusable in `cmd/replay` and in offline recomputation of historical scores. **Lost:** any value that would require a live lookup (e.g., a truly dynamic repo percentile computed at score time rather than pre-computed as an input) must be computed **before** calling `scoring.Score` and passed in as part of `ScoringInput`/`Signals`, not fetched inside it.

### Consequences
Anything scoring needs (repo churn percentile, blast-radius inputs) must be fully resolved by the `signals` package **before** the scoring call — this is precisely why Phase 4 (signals) precedes Phase 6 (scoring) in the development plan.

---

## ADR-008: Check Run Conclusion Is Never `failure`

### Context
GitHub Check Runs can conclude with `success`, `neutral`, or `failure` (among others); a `failure` conclusion can be configured by repo admins to block merges.

### Decision
The system's check run conclusion is always `success` (low/moderate band) or `neutral` (high/critical band) — never `failure`.

### Alternatives
- Conclude `failure` on high/critical-risk PRs to actively gate merges.
- Make the conclusion policy configurable per repo from day one.

### Reasoning
`plan.md` §7: *"Conclusion policy — `success` for low/moderate, `neutral` for high/critical (never `failure`; do not block merges, at least until the tool has earned trust; make it configurable later)."* §17 reinforces the cost of getting this wrong: *"Setting the check run conclusion to `failure`. You just blocked merges on your bot's opinion, and it will be uninstalled the first time it's wrong."*

### Trade-offs
**Gained:** an unproven, occasionally-wrong bot cannot block a team's merges, which is the single most common reason a new review bot gets uninstalled. **Lost:** the tool cannot function as a hard merge gate even for teams that would want that — `plan.md` explicitly defers this to "later," and this documentation package treats it as out of MVP+V1 scope entirely (see [PRD.md Non-Goals](./PRD.md#4-non-goals)).

### Consequences
No API surface, config field, or UI control exists in the MVP/V1 scope for changing this policy — introducing one prematurely would contradict this decision's own stated trust-building rationale.

---

## ADR-009: Dual-Guard Idempotency (Queue Unique Key + Database Partial Unique Index)

### Context
GitHub webhooks are delivered at-least-once and can be redelivered; multiple workers can race to process the same task.

### Decision
Idempotency is enforced at two independent layers: an `asynq` unique-task lock keyed by `sha256(installation|repo|pr|head_sha|mode)`, **and** a partial unique index on `reviews(pull_request_id, head_sha, review_mode) WHERE status <> 'superseded'`.

### Alternatives
- Rely on the queue-level guard alone.
- Rely on the database constraint alone, with no queue-level deduplication.

### Reasoning
`plan.md` §3 states the database index is "your idempotency guarantee at the data layer" specifically because the queue guard alone cannot cover every race — e.g., a Redis failover or a manual re-trigger that bypasses the queue path entirely. `plan.md` §16 item 4 places this among the highest-priority early decisions: *"Idempotency in Phase 2, before anything is expensive... discovering the problem via your bill"* is the failure mode being avoided.

### Trade-offs
**Gained:** idempotency holds even if one layer is bypassed or fails (defense in depth against duplicate, costly LLM spend). **Lost:** two mechanisms to reason about instead of one; a developer must understand that either guard alone is not "the" idempotency mechanism.

### Consequences
Any new manual trigger path (e.g., a future admin "force re-run" feature) must still go through both the standard idempotency-key derivation and the same `reviews` insert path — it cannot bypass either guard, by construction of the shared `store.Persist`/`queue.Enqueue` helpers.

---

## ADR-010: Provider-Agnostic LLM Interface, Single Provider for MVP

### Context
The system depends on an LLM for finding generation, but LLM providers vary in API shape, structured-output support, pricing, and reliability.

### Decision
Define a `llm.Provider` interface up front (`plan.md` §1: "Provider-agnostic interface. Start with one provider. The interface is what lets you do model routing later."), but implement only one concrete provider for the MVP (Phase 5); defer a second provider to Phase 8.

### Alternatives
- Build against a single provider's SDK directly with no abstraction, generalizing later if needed.
- Build multi-provider support and routing from the start.

### Reasoning
`plan.md` §16 explicitly names premature multi-provider abstraction as something to defer aggressively: *"the abstraction only reveals its real shape after one provider is fully working."* At the same time, `plan.md` §1 calls for the interface to exist from the start specifically so model routing (Phase 8) doesn't require a rewrite of every call site.

### Trade-offs
**Gained:** the interface boundary is correct from day one (informed by real usage against one provider) without paying the cost of maintaining and testing two providers before either is proven. **Lost:** the interface's shape is a best guess until a second provider is actually implemented in Phase 8 — some rework of the interface at that point is accepted as likely, not treated as a failure.

### Consequences
`llm.StubProvider` (used by `cmd/replay` and all offline tests) implements the same interface from Phase 5 onward, so the interface is exercised by two implementations (real + stub) well before a second real provider exists — this substantially de-risks the eventual Phase 8 addition.

---

## Decisions Deliberately Not Recorded as ADRs

Per the instruction to skip trivial decisions: choice of Gin over another Go HTTP framework, choice of React/Tailwind/Vite for the frontend, and choice of `golang-migrate` vs. `goose` are all stated once in `plan.md` §1 with a one-line rationale and do not involve a meaningful trade-off analysis or a one-way door — they are conventional, easily-reversible tooling choices, not architecture.
