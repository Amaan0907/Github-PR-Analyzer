# Development Plan — GitHub PR Risk Analyzer

**Derived from:** [PRD.md](./PRD.md) · **Source of truth:** [`../pr-risk-analyzer-plan.md`](../pr-risk-analyzer-plan.md) §13, §16, §18
**Related:** [architecture-decisions.md](./architecture-decisions.md) · [requirements.md](./requirements.md) · [testing-strategy.md](./testing-strategy.md)

This is an implementation roadmap, not a design document — it orders work by technical dependency, per `plan.md` §16 ("Development Order to Minimize Rework"). Phase numbering matches `plan.md` §13 exactly so the two documents stay directly cross-referenceable.

---

## Ordering Principles (why this order, not another)

Nine constraints from `plan.md` §16 shape every phase boundary below:

1. The finding JSON schema is frozen before any prompt is written — validation, storage, scoring, comments, and UI all depend on it.
2. The diff position map is built in Phase 3, not deferred to Phase 7 — retrofitting line mapping into a working pipeline is expensive, and every finding needs anchoring anyway.
3. Scoring is pure from its first line — one DB "just this once" call and it becomes untestable.
4. Idempotency is built in Phase 2, before anything is expensive — adding it after real LLM spend means discovering the problem via the bill.
5. The stub LLM provider and `cmd/replay` exist before the real provider.
6. Dry-run publishing exists from day one of Phase 7.
7. Deterministic signals (Phase 4) are built before the LLM (Phase 5) — this forces the "LLM is one signal among several" architecture; reversed, the LLM ends up doing everything.
8. `analyze` and `publish` are separate tasks from the start.
9. `scoring_version`/`prompt_version` columns exist from Phase 6 even with only one version of each — they cannot be backfilled later.

The dashboard (Phase 9) and multi-provider support (Phase 8+) are deliberately deferred: there is nothing to display until Phase 7 produces real results, and a multi-provider abstraction only reveals its true shape after one provider works end to end.

---

## Phase 0 — Foundations

**Objective:** a running skeleton with database, queue, config, logging, CI, and local dev environment. No GitHub, no AI.
**Dependencies:** none — this is the starting point.
**Tasks:** Go module init; `cmd/api` with Gin and graceful shutdown; typed config that fails loudly on missing required vars; `store` package with pool setup and a `WithTx` helper; `/healthz` and `/readyz` (DB + Redis); request-ID/logging/recovery middleware; Makefile; multi-stage Dockerfile; Docker Compose (Postgres + Redis); CI running lint, vet, race tests; migrations for `installations`, `repositories` (minimal columns).
**Expected output:** `docker compose up` yields a service answering `/readyz` with DB and Redis status; migrations run both directions; CI is green on a clean clone.
**Definition of done:** matches `plan.md` §13 Phase 0 "Done when" exactly.

## Phase 1 — GitHub App and Webhook Ingestion

**Objective:** receive, verify, persist, and acknowledge real GitHub events. Still no analysis.
**Dependencies:** Phase 0 (DB, config, logging).
**Tasks:** register the App against a test org with the permissions in [HLD.md §7](./HLD.md#7-authentication-flow); `POST /webhooks/github` (raw-body read → HMAC verify → delivery-ID dedupe → persist → route → 202); `installation`/`installation_repositories` lifecycle handlers; PR/repo upsert logic; `github.App` (JWT mint + Redis-cached installation tokens); one smoke call fetching a PR and logging title + file count.
**Expected output:** opening/updating/closing a PR on the test repo produces correctly persisted rows.
**Definition of done:** signature rejection works, redeliveries are no-ops, every response is under a second (matches FR-001–FR-009).

## Phase 2 — Queue, Worker, and Review Lifecycle

**Objective:** async execution with idempotency, retries, and visible state, using a placeholder analysis.
**Dependencies:** Phase 1 (events must exist to enqueue against).
**Tasks:** task definitions/payloads in `internal/queue`; idempotency key derivation, `asynq.TaskID` + `Unique`; `cmd/worker` with concurrency/priority/retry/panic-recovery/graceful-shutdown; `review:analyze` handler that transitions `queued → running`, produces a placeholder score, transitions to `completed`; check run created `in_progress` and updated at end via `defer`; supersession on `synchronize` with the Redis cancel-flag mechanism; scheduled stale-review reaper.
**Expected output:** three rapid pushes to one PR yield three reviews, first two `superseded`, only the last completes; check runs always reach a terminal state; redelivered webhooks never double-run.
**Definition of done:** matches FR-011–FR-020 acceptance criteria; concurrent-duplicate-enqueue integration test passes.

## Phase 3 — PR Data Fetching, Diff Parsing, and Context Building

**Objective:** turn a PR into a rich, structured, budgeted analysis context. Deterministic, offline-testable, no AI.
**Dependencies:** Phase 1 (GitHub client/token flow already exists).
**Tasks:** `github.GetPRDiff`/`GetPRFiles` with pagination and 300-file-truncation detection; `diff.Parse` into structured files/hunks; `diff.BuildPositionMap` with rename handling; `diff.Classify`; `context.Build` (file selection, concurrent content fetch, neighbor resolution, redaction, token budgeting, truncation flags); persist `file_impacts`; build `cmd/replay` against a saved diff, no network.
**Expected output:** `cmd/replay` emits a complete context bundle with correct positions and zero network calls; the position map is verified against at least one live PR by manual comment placement.
**Definition of done:** matches FR-021–FR-030; parser fixtures (renames, multi-hunk, EOF, CRLF, binary) all pass.

## Phase 4 — Deterministic Signal Layer

**Objective:** produce meaningful risk signals with no AI at all — the architectural forcing function that keeps the LLM as one signal among several, not the whole system.
**Dependencies:** Phase 3 (needs classified files and diff structure).
**Tasks:** `signals.Heuristics` (size, churn percentile, test ratio, migration/config/CI detection); `signals.SensitivePaths` with `.prrisk.yml` overrides; `signals.Deps` (manifest diffing, major/minor/patch classification); `signals.TestGap`; `signals.Churn` (optionally cached in `file_stats`); `signals.Secrets`; optional sandboxed static-analyzer runner; config loader with schema validation and safe defaults.
**Expected output:** a review produces a populated `signals` blob and non-LLM findings (test gaps, dependency risk, secrets) with zero LLM involvement.
**Definition of done:** matches FR-031–FR-040. **Checkpoint (`plan.md` §18):** confirm the tool produces useful output with the LLM entirely disabled — if not, the signal layer is too thin and must be strengthened before Phase 5.

## Phase 5 — LLM Integration and Structured Findings

**Objective:** one provider, one mode (`standard`), producing schema-valid, evidence-anchored findings.
**Dependencies:** Phase 3 (context bundles) + Phase 4 (signals feed the synthesis call and confidence corroboration).
**Tasks:** `llm.Provider` interface + one concrete implementation (timeouts, retry on 429/5xx, structured output); versioned prompt templates ([LLD.md §4.2](./LLD.md#42-frozen-finding-json-schema-fr041) schema frozen **before** this); chunker respecting file boundaries and budget; map stage (`errgroup`, bounded concurrency, tolerated per-chunk failure); reduce/synthesis stage; `findings.Validate`/`Dedupe`/`Calibrate`/`Cap`; every call recorded in `llm_calls` including failures; stub provider replaying fixtures.
**Expected output:** `cmd/replay` produces validated, anchored, deduplicated findings offline in under a second with the stub provider; a planted fabricated finding is demonstrably filtered.
**Definition of done:** matches FR-041–FR-055.

## Phase 6 — Risk Scoring Engine

**Objective:** a deterministic, explainable, versioned 0–100 score with category breakdown.
**Dependencies:** Phase 4 (signals) + Phase 5 (findings) — scoring consumes both, produces neither.
**Tasks:** `internal/scoring` package (`ScoringInput`/`ScoringResult`/`Contribution`), implementing the formula in [LLD.md §4.5](./LLD.md#45-risk-scoring-fr056fr061) exactly; weights/constants in a versioned struct, not scattered literals; `scoring_version` constant; per-file risk attribution; wire into the pipeline after finding validation; write `docs/scoring.md` (implementation-facing formula doc, distinct from this documentation package) and commit it.
**Expected output:** scoring is a pure function with no non-stdlib imports; every score has a breakdown explaining each contribution.
**Definition of done:** matches FR-056–FR-065; golden files across all four bands committed; determinism test at 1000 iterations passes.

## Phase 7 — GitHub Publishing (MVP complete)

**Objective:** deliver results into the PR the way a real tool does. **This is the MVP boundary.**
**Dependencies:** Phase 6 (a score must exist to publish) + Phase 2 (the `publish` task type already exists, unused until now).
**Tasks:** `review:publish` task (enqueued by `review:analyze`, never re-running analysis on its own failure); check-run output rendering; conclusion policy (`success`/`neutral`, never `failure`); summary-comment upsert via hidden marker; inline comments (batched review submission, `line`/`side`/`start_line`, `anchored && confidence≥0.75 && severity≥medium`, capped at 10, `422` demoted to summary); `PRRISK_DRY_RUN` mode; markdown rendering helpers.
**Expected output:** opening a PR on the test repo produces, within ~60 seconds with no human action, a check run, an upserted summary comment, and correctly-placed inline comments.
**Definition of done:** matches FR-066–FR-075. **Record a demo now** — per `plan.md` §13 Phase 7, this is the interview/portfolio artifact.

> ### MVP Completion Point
> **End of Phase 7.** Everything above (Phases 0–7) is the MVP: GitHub App, webhooks, async worker, diff/context extraction, deterministic signals, LLM findings with validation, deterministic scoring, check run + comments, standard mode only, one LLM provider, no dashboard. Per `plan.md` §14 and §18: *"M1–M7 alone (about 10 weeks) is a complete, defensible, portfolio-grade project."*

---

## Post-MVP Work

### Phase 8 — Review Modes and Model Routing (V1)
**Dependencies:** Phase 5 (LLM layer) + Phase 6 (scoring must accept mode-adjustable weights).
**Tasks:** six mode definitions; `llm.Router` (complexity → model); escalation path; three-level budget enforcement with degradation; `/prrisk` slash-command parsing with write-access checks; config precedence chain (`repo > installation > defaults`); second provider implementation.
**Expected output:** `/prrisk review --mode=deep` runs the appropriate model/context depth; cost per mode is measurably different; exceeding budget degrades gracefully.
**Definition of done:** matches FR-076–FR-085.

### Phase 9 — Dashboard: Core (V1)
**Dependencies:** Phase 7 (there must be real reviews to display — building UI against an unstable pre-Phase-7 schema wastes time per `plan.md` §16).
**Tasks:** GitHub OAuth login/callback/session; dashboard REST endpoints (installations, repos, reviews, findings, impacts, llm-calls, rerun) with tenant scoping enforced at the store layer; React/TS/Tailwind/Vite frontend — auth flow, installation switcher, reviews list (filters, cursor pagination), review detail (score breakdown, findings, impacts, cost panel), re-run trigger.
**Expected output:** a user logs in with GitHub, sees only their installations' reviews, opens a review, and understands why it scored what it scored without reading code.
**Definition of done:** matches FR-086–FR-095.

### Phase 10 — Analytics and Cost Intelligence (V1)
**Dependencies:** Phase 9 (dashboard shell must exist to host analytics views).
**Tasks:** `/analytics/overview`, `/analytics/cost`, `/analytics/hotspots` endpoints; supporting indexes; optional materialized view if query latency crosses ~200ms; analytics frontend page (date-range picker, stat cards, trend chart, cost breakdown, hotspot table).
**Expected output:** cost/model/hotspot/trend questions answerable from the UI in under 10 seconds.
**Definition of done:** matches FR-096–FR-100.

### Phase 11 — Reliability Hardening (V2)
**Dependencies:** Phases 0–10 (this phase hardens the existing system rather than adding features).
**Tasks:** full error-taxonomy implementation across every external call site; GitHub rate-limit-aware throttling; circuit breaker per LLM provider with fallback/degradation; dead-letter handling with replay; Prometheus metrics; `k6`/`vegeta` load test (100 concurrent webhooks) with tuning; chaos checks (worker kill mid-review, Redis severed, garbage LLM forever); `docs/runbook.md`.
**Expected output:** every documented failure mode has a test; load-test numbers are documented.
**Definition of done:** matches FR-101–FR-110.

### Phase 12 — Deployment, Evaluation, and Polish (V2)
**Dependencies:** Phase 11 (deploying an unhardened system is premature).
**Tasks:** production Dockerfiles/deploy config for both binaries; managed Postgres (backups) + Redis (persistence); migration step in the deploy pipeline; secrets via platform secret store; evaluation harness run against 25–40 labeled public PRs, results committed; README (architecture diagram, demo, scoring formula, eval results, load-test numbers); `docs/architecture.md`, `docs/runbook.md`, ADRs.
**Expected output:** a stranger can install the App on their repo and get a real review; the dashboard is live; the README leads with evaluation results.
**Definition of done:** matches FR-110, FR-113 (retention live in production), and `plan.md` §13 Phase 12 "Done when."

---

## First Thing to Implement

**Phase 0's `/readyz` endpoint plus the Docker Compose stack.** Nothing else — including the webhook handler — can be meaningfully built or tested without a running database and queue to persist against.

## First Complete Vertical Slice

**End of Phase 2:** a real GitHub PR event flows end-to-end through verification → persistence → queue → a (placeholder-scored) worker run → a check run visible on the PR, with idempotency and supersession already correct. This is the first point at which "open a PR, see something happen on GitHub" is true, even though the "something" isn't a real analysis yet.

## MVP Completion Point

**End of Phase 7** (see callout above) — real diff analysis, real LLM findings, real deterministic scoring, real check run/comments, no dashboard required.

## Post-MVP Work

Phases 8–12, summarized above: review modes and routing, the dashboard, analytics, reliability hardening, and public deployment with a published evaluation harness.
