# Requirements Specification — GitHub PR Risk Analyzer

**Derived from:** [PRD.md](./PRD.md) · **Source of truth:** [`../pr-risk-analyzer-plan.md`](../pr-risk-analyzer-plan.md)
**Related:** [user-flows.md](./user-flows.md) · [API.md](./API.md) · [database-design.md](./database-design.md)

Each requirement lists its priority tag: **(MVP)** = `plan.md` Phases 0–7, **(V1)** = Phases 8–10, **(V2)** = Phases 11–12.

---

## Functional Requirements

### Webhook Ingestion (FR-001–FR-010)

**FR-001 — Verify webhook signature (MVP)**
*User story:* As the system, I must reject any webhook payload that isn't verifiably from GitHub, so that no unauthenticated party can trigger analysis or spend.
*Acceptance criteria:*
- HMAC-SHA256 is computed over the **raw** request body and compared to `X-Hub-Signature-256` using `hmac.Equal` (constant-time). (`plan.md` §5, §10)
- Missing or invalid signature → `401`, request is not persisted.
- No configuration flag exists that can disable verification in any environment, including local dev. (`plan.md` §10)

**FR-002 — Deduplicate webhook deliveries (MVP)**
*Acceptance criteria:*
- `X-GitHub-Delivery` is checked against a unique index on `webhook_events` before processing.
- A previously-seen delivery ID returns `200` immediately with no further side effects. (`plan.md` §5 step 3)

**FR-003 — Acknowledge within GitHub's timeout budget (MVP)**
*Acceptance criteria:*
- The webhook handler performs no diff fetching, no LLM calls, and no scoring inline.
- Response is returned in under 1 second under normal load. (`plan.md` §1 rule 1)

**FR-004 — Route events by type and action (MVP)**
*Acceptance criteria:*
- `pull_request` (`opened`, `reopened`, `ready_for_review`) → enqueue a review.
- `pull_request` (`synchronize`) → supersede in-flight review(s), enqueue a new one against the new head SHA.
- `pull_request` (`closed`) → update PR state, cancel any in-flight review.
- `issue_comment` (`created`, body starts with `/prrisk`, issue is a PR) → parse mode, enqueue manual review. **(V1)**
- `installation` / `installation_repositories` events → upsert/deactivate installations and repositories. (`plan.md` §5 "Subscribed events")

**FR-005 — Record skip conditions, not silent drops (MVP)**
*Acceptance criteria:*
- Draft PRs (unless configured otherwise), bot authors, all-generated/vendored/lockfile-only diffs, oversized PRs, and inactive repos produce a `reviews` row with `status = 'skipped'` and a reason, not nothing. (`plan.md` §5 "Skip conditions")

**FR-006 — Enforce request body size limit (MVP)**
*Acceptance criteria:*
- `http.MaxBytesReader` (or equivalent) rejects oversized payloads before they are read into memory. (`plan.md` §10)

**FR-007 — Mint and cache installation tokens (MVP)**
*Acceptance criteria:*
- App JWT (RS256, ≤10 min expiry) is minted from the private key per `installations`.
- Installation tokens are cached in Redis with TTL = expiry − 5 minutes, refreshed on `401`. (`plan.md` §5 "Token strategy")

**FR-008 — Upsert installation/repository/PR state on relevant events (MVP)**
*Acceptance criteria:* rows in `installations`, `repositories`, `pull_requests` reflect the latest event payload; no duplicate rows are created for the same GitHub entity (unique constraints on GitHub-native IDs).

**FR-009 — Support installation lifecycle events (MVP)**
*Acceptance criteria:* `installation.deleted` deactivates the installation and triggers the data-retention deletion path (see FR-102). `installation.suspend`/`unsuspend` set/clear `suspended_at`.

**FR-010 — Slash-command authorization (V1)**
*Acceptance criteria:* `/prrisk` commands are only honored from commenters with write access to the repository; unauthorized attempts are ignored (not error-reported to the PR) and logged.

---

### Queue, Idempotency, and Review Lifecycle (FR-011–FR-020)

**FR-011 — Idempotent enqueue by `(installation, repo, pr, head_sha, mode)` (MVP)**
*Acceptance criteria:*
- Idempotency key = `sha256(installation_id|repo_id|pr_number|head_sha|mode)`.
- Enqueue uses this as the `asynq.TaskID` with `Unique(24h)`.
- `ErrTaskIDConflict` results in a `200` no-op response, not an error. (`plan.md` §5 step 8)

**FR-012 — Database-level idempotency guard (MVP)**
*Acceptance criteria:* a partial unique index on `reviews(pull_request_id, head_sha, review_mode) WHERE status <> 'superseded'` prevents two live reviews for the same commit even under a race the queue guard didn't catch. (`plan.md` §3)

**FR-013 — Guarded state transitions (MVP)**
*Acceptance criteria:* every status change is a conditional `UPDATE ... WHERE status = <expected>`; a transition affecting zero rows is treated as "another worker already handled this" and aborts cleanly, not as an error. (`plan.md` §8 "State machine")

**FR-014 — Supersession on new push (MVP)**
*Acceptance criteria:*
- On `synchronize`, all `queued`/`running` reviews for that PR are marked `superseded`.
- A Redis cancellation flag is set; the worker checks it between pipeline stages (never mid external call) and stops early if set.
- The publish stage re-checks status immediately before posting and aborts if superseded. (`plan.md` §8 "Supersession")

**FR-015 — Stale-review reaping (MVP)**
*Acceptance criteria:* a scheduled task marks any review `running` beyond its task timeout as `failed` with `error_code = STALE`. (`plan.md` §8)

**FR-016 — Separate `analyze` and `publish` tasks (MVP)**
*Acceptance criteria:* a failure in the publish stage never re-triggers the analyze stage; each is independently retryable with its own timeout and retry budget. (`plan.md` §8 "Task types", §16 item 8)

**FR-017 — Retry policy by error class (MVP)**
*Acceptance criteria:* retryable errors back off exponentially with jitter (`min(2^n × 5s, 5min)`) up to the task's max retries; terminal errors fail immediately without consuming retry budget. (`plan.md` §8, §11)

**FR-018 — Priority queues (MVP)**
*Acceptance criteria:* manual-trigger reviews run on a `critical` queue; automatic reviews on `default`; maintenance tasks on `low`, with weighted non-strict priority (`critical:6, default:3, low:1`). (`plan.md` §8)

**FR-019 — Panic recovery (MVP)**
*Acceptance criteria:* a panic in any worker handler is recovered, the review is marked `failed`, the error is reported to observability, and the task is **not** retried. (`plan.md` §8, §11)

**FR-020 — Concurrency bounding (MVP)**
*Acceptance criteria:* file fetches and LLM chunk analysis run under `errgroup` with a bounded semaphore; no unbounded goroutine fan-out exists in the pipeline. (`plan.md` §16 item, "Queue and reliability" mistakes list)

---

### Diff Parsing and Context Building (FR-021–FR-030)

**FR-021 — Fetch full diff and file list with pagination (MVP)**
*Acceptance criteria:* handles the GitHub files-endpoint 300-file cap; sets `stats.files_truncated` when detected rather than silently losing data. (`plan.md` §3, §11)

**FR-022 — Parse unified diffs into structured hunks (MVP)**
*Acceptance criteria:* correctly handles renames, multi-hunk files, additions at EOF, no-trailing-newline markers, CRLF, and binary entries, verified against fixtures. (`plan.md` §3, §12)

**FR-023 — Build a `(file, new_line) → diff position` map (MVP)**
*Acceptance criteria:* map is built once per review and reused by both finding-anchoring and comment-posting; correctness verified against at least one live PR by manual comment placement. (`plan.md` §3, §13 Phase 3 "Done when")

**FR-024 — Classify every changed file (MVP)**
*Acceptance criteria:* each file is labeled source/test/config/migration/generated/vendored/lockfile/docs/binary using path patterns plus `.gitattributes` `linguist-generated` where available. (`plan.md` §6 Stage 1)

**FR-025 — Exclude non-source content from LLM context (MVP)**
*Acceptance criteria:* binaries, generated files, vendored files, lockfiles, and files over a configured size cap are never sent to the LLM as full content. (`plan.md` §6 Stage 1)

**FR-026 — Fetch full file content at head SHA for remaining files (MVP)**
*Acceptance criteria:* the model receives full surrounding context, not diff-only, for files within the context budget. (`plan.md` §6 Stage 1)

**FR-027 — Resolve neighbor files (MVP)**
*Acceptance criteria:* sibling test files, files importing a changed file (naive per-language grep resolution acceptable), and referenced type/interface definitions are pulled into context when budget allows. (`plan.md` §6 Stage 1)

**FR-028 — Redact secrets before any content leaves the process (MVP)**
*Acceptance criteria:* high-entropy strings and known token patterns (`sk-`, `ghp_`, `AKIA`, PEM blocks, `.env` values, connection strings) are replaced with a `[REDACTED:<type>]` marker before inclusion in any LLM prompt. (`plan.md` §10)

**FR-029 — Token budgeting with explicit truncation flags (MVP)**
*Acceptance criteria:* files are ranked by `changed_lines × sensitivity`; the top N get full content, the rest diff-only; when the budget is exceeded, `stats.context_truncated = true` is recorded — truncation is never silent. (`plan.md` §6 Stage 1 step 7, §17 "Diff and context" mistakes)

**FR-030 — Offline replay capability (MVP)**
*Acceptance criteria:* `cmd/replay` runs the full context-building stage against a saved diff fixture with zero network calls. (`plan.md` §9, §13 Phase 3 "Done when")

---

### Deterministic Signal Layer (FR-031–FR-040)

**FR-031 — Size/scope metrics (MVP)**
*Acceptance criteria:* additions, deletions, files changed, languages touched, and changed-lines percentile vs. repo history are computed with no LLM involvement. (`plan.md` §13 Phase 4)

**FR-032 — Sensitive-path detection (MVP)**
*Acceptance criteria:* configurable glob rules (auth, payments, migrations, CI, infra, public API) flag files as sensitive with recorded reasons; overridable per repo via `.prrisk.yml`. (`plan.md` §13 Phase 4)

**FR-033 — Dependency-change analysis (MVP)**
*Acceptance criteria:* manifest diffs (`go.mod`, `package.json`, `requirements.txt`, `pom.xml`, etc.) are classified major/minor/patch and new-direct-dependency additions/removals are flagged. (`plan.md` §13 Phase 4)

**FR-034 — Test-gap detection (MVP)**
*Acceptance criteria:* for each substantially changed source file, the presence/absence of a corresponding changed test file is checked; a `test_gap` finding is emitted with `source = heuristic` when absent. (`plan.md` §13 Phase 4)

**FR-035 — Churn signal (MVP)**
*Acceptance criteria:* commits touching each changed file in the last 90 days are counted (optionally cached in `file_stats`) and used in blast-radius scoring. (`plan.md` §3, §7)

**FR-036 — Secret detection as a finding source (MVP)**
*Acceptance criteria:* entropy/pattern-based detection emits `critical` findings with `source = static_analysis`, independent of the LLM. (`plan.md` §13 Phase 4)

**FR-037 — Optional sandboxed static analysis (Post-MVP enhancement)**
*Acceptance criteria:* if enabled, analyzers (e.g., `semgrep`, `gitleaks`) run in a network-isolated, read-only, resource-capped, timed-out container; a crash or timeout degrades the review, it never fails it. (`plan.md` §10 "Static analysis sandboxing", §13 Phase 4)

**FR-038 — Per-repo configuration loading (MVP)**
*Acceptance criteria:* `.prrisk.yml` is parsed with schema validation and safe defaults; an invalid config does not break analysis (falls back to defaults, reports the error). (`plan.md` §13 Phase 4, Phase 8)

**FR-039 — Signal-only operation (MVP)**
*Acceptance criteria:* a review with the LLM entirely disabled still produces a populated `signals` blob and non-LLM findings. (`plan.md` §13 Phase 4 "Done when", §18 "Checkpoints")

**FR-040 — CODEOWNERS-derived reviewer context (V1)**
*Acceptance criteria:* `.github/CODEOWNERS` is read and used for ownership context in prompts and reviewer-suggestion rationale. (`plan.md` §6 "Prompt structure", §15 "Should Have")

---

### LLM Analysis and Findings (FR-041–FR-055)

**FR-041 — Fixed, versioned finding schema (MVP)**
*Acceptance criteria:* the JSON contract in `plan.md` §6 Stage 3 is frozen before any prompt is written; every provider is coerced into it via structured output / JSON mode / tool-calling, never "please respond with JSON." (`plan.md` §6 Stage 3, §16 item 1)

**FR-042 — One repair attempt on malformed output (MVP)**
*Acceptance criteria:* a parse failure triggers exactly one repair call with the parse error fed back; a second failure marks the stage `status = invalid_json` in `llm_calls` and the chunk is tolerated as failed, not retried indefinitely. (`plan.md` §6 Stage 3, §17 "LLM" mistakes)

**FR-043 — Temperature 0, pinned model versions (MVP)**
*Acceptance criteria:* no floating model alias is used in production; temperature is 0 or the lowest supported value. (`plan.md` §6 Stage 3)

**FR-044 — Evidence-based hallucination filtering (MVP)**
*Acceptance criteria:* a finding is dropped if its `evidence` string does not appear verbatim (or near-verbatim per FR-047) in the file content actually sent to the model. (`plan.md` §6 Stage 4, §19 item 2)

**FR-045 — File-existence validation (MVP)**
*Acceptance criteria:* a finding referencing a `file_path` not in the changed-file set is dropped. (`plan.md` §6 Stage 4)

**FR-046 — Hunk-membership anchoring (MVP)**
*Acceptance criteria:* a finding whose `start_line` isn't within an added/modified hunk is kept for the summary/dashboard but marked `anchored = false` and is never eligible for an inline comment. (`plan.md` §6 Stage 4)

**FR-047 — Deterministic confidence calibration (MVP)**
*Acceptance criteria:* `final_confidence` is computed from `model_confidence` via the fixed multiplier formula in `plan.md` §6 Stage 4 (loose-match, corroboration, truncated-context, cross-file penalties), clamped to `[0.05, 0.95]`. Model self-reported confidence is never used unmodified. (`plan.md` §6 Stage 4, §17 "LLM" mistakes)

**FR-048 — Deduplication (MVP)**
*Acceptance criteria:* findings sharing a `dedupe_hash` (file + rule/title + normalized snippet) are merged, keeping the highest severity. (`plan.md` §3, §6 Stage 4)

**FR-049 — Style/nit suppression by default (MVP)**
*Acceptance criteria:* style-category findings are dropped unless the active mode explicitly opts in. (`plan.md` §6 Stage 4)

**FR-050 — Finding and per-file caps (MVP)**
*Acceptance criteria:* total findings capped at 25, per-file at 5, retaining highest severity × confidence when trimming. (`plan.md` §13 Phase 5)

**FR-051 — Map-reduce chunking (MVP)**
*Acceptance criteria:* files are grouped into chunks sized to 40–60% of the model's practical context window; chunks are analyzed in parallel (bounded concurrency); a single reduce/synthesis call produces the PR summary from deduped finding summaries plus signals — the synthesis call never re-derives the score. (`plan.md` §6 Stage 2)

**FR-052 — Full observability of every LLM call (MVP)**
*Acceptance criteria:* every call (including failed attempts and repairs) is recorded in `llm_calls` with provider, model, token counts, cost, latency, attempt number, and status. (`plan.md` §3, §11)

**FR-053 — Prompt injection resistance (MVP)**
*Acceptance criteria:* PR content is wrapped in delimited blocks and the system prompt explicitly states that content is data, not instruction; the model is given no tools and no network access; model output never directly controls what gets posted. (`plan.md` §10 "Prompt injection")

**FR-054 — Stub provider for offline development (MVP)**
*Acceptance criteria:* a stub `llm.Provider` implementation replays recorded fixtures from `testdata/llm_responses/`, enabling `cmd/replay` to run with zero spend and zero network calls. (`plan.md` §16 item 5)

**FR-055 — Versioned prompts (MVP)**
*Acceptance criteria:* prompt templates are files (e.g., `prompts/standard.v3.tmpl`), embedded, and the prompt version used is stored on the `reviews` row. (`plan.md` §6 "Prompt structure")

---

### Risk Scoring (FR-056–FR-065)

**FR-056 — Pure scoring function (MVP)**
*Acceptance criteria:* `internal/scoring` imports nothing beyond the Go standard library; the function performs zero I/O. (`plan.md` §7, §16 item 3)

**FR-057 — Category subscores with saturation (MVP)**
*Acceptance criteria:* per-category score computed via `100 × (1 − exp(−raw_c / K_c))` so that many low-severity findings don't equal one critical finding. (`plan.md` §7)

**FR-058 — Blast-radius multiplier (MVP)**
*Acceptance criteria:* computed independently of any finding, from sensitive paths, migrations, dependency changes, changed-line percentile, test ratio, churn, and fan-in, capped at 1.6. (`plan.md` §7)

**FR-059 — Final score combination and banding (MVP)**
*Acceptance criteria:* `score = round(min(100, base × blast))`; bands are 0–24 low, 25–49 moderate, 50–74 high, 75–100 critical. (`plan.md` §7)

**FR-060 — Floors and ceilings (MVP)**
*Acceptance criteria:* a critical finding with confidence ≥0.8 floors the score at 75; a high-severity security finding with confidence ≥0.8 floors it at 60; zero findings with blast ≤1.1 and <30 changed lines ceilings it at 15. (`plan.md` §7)

**FR-061 — Full contribution breakdown (MVP)**
*Acceptance criteria:* every non-zero term feeding the final score is emitted as a `Contribution` entry, sufficient to render "why this score" without reading code. (`plan.md` §7, §13 Phase 6)

**FR-062 — Scoring version snapshot (MVP)**
*Acceptance criteria:* every review stores `scoring_version` and the raw `signals` blob so any historical score can be recomputed offline. (`plan.md` §3, §7)

**FR-063 — Golden-file regression tests (MVP)**
*Acceptance criteria:* ~20 fixture inputs covering every band and floor/ceiling case are committed with expected outputs; a weight change requires intentionally updating goldens. (`plan.md` §7, §12)

**FR-064 — Determinism guarantee (MVP)**
*Acceptance criteria:* identical `ScoringInput`, run 1000 times, produces byte-identical `ScoringResult`. (`plan.md` §12 "Rules")

**FR-065 — Monotonicity properties (MVP)**
*Acceptance criteria:* score is monotonic in severity (upgrading a finding's severity never decreases the score) and additive in findings (adding a finding never decreases the score); output always in `[0, 100]`. (`plan.md` §12 "Layers" property tests)

---

### GitHub Publishing (FR-066–FR-075)

**FR-066 — Check run lifecycle (MVP)**
*Acceptance criteria:* a check run is created `in_progress` at analysis start and always reaches a terminal conclusion (`success` or `neutral`, never `failure`), guaranteed via `defer` even on unexpected exit paths. (`plan.md` §7, §11 "Never leave a check run stuck")

**FR-067 — Conclusion policy (MVP)**
*Acceptance criteria:* `success` for low/moderate bands, `neutral` for high/critical — never `failure`. (`plan.md` §7 "Conclusion policy")

**FR-068 — Sticky summary comment (MVP)**
*Acceptance criteria:* the summary comment is upserted via a hidden HTML marker (search-then-PATCH-or-POST); repeated pushes update one comment, never create duplicates. (`plan.md` §7, §17 "Webhooks and GitHub" mistakes)

**FR-069 — High-confidence inline comments only (MVP)**
*Acceptance criteria:* only findings with `anchored = true`, `confidence ≥ 0.75`, and `severity ≥ medium` are eligible; capped at 10 per PR, ordered by severity × confidence. (`plan.md` §6 Stage 4, §7)

**FR-070 — Batch comment submission (MVP)**
*Acceptance criteria:* inline comments are submitted as a single PR review (`event: COMMENT`) using `line`/`side`/`start_line`, not one POST per comment. (`plan.md` §7)

**FR-071 — Graceful handling of comment placement failure (MVP)**
*Acceptance criteria:* a `422` on an individual comment demotes that finding into the summary instead of failing the whole review. (`plan.md` §7, §11)

**FR-072 — Dry-run mode (MVP)**
*Acceptance criteria:* `PRRISK_DRY_RUN=true` logs the exact payloads that would be sent instead of calling GitHub. (`plan.md` §7, §16 item 6)

**FR-073 — Publish re-checks supersession before posting (MVP)**
*Acceptance criteria:* the publish task aborts without posting if the review's status has become `superseded` since it was queued. (`plan.md` §8 "Supersession")

**FR-074 — Comment budget enforcement (MVP)**
*Acceptance criteria:* total comments posted per PR never exceeds the configured cap (default 10–15 per `plan.md` §10; see PRD Assumption A6). (`plan.md` §10 "Abuse and cost")

**FR-075 — Markdown rendering helpers (MVP)**
*Acceptance criteria:* summary and inline comments use consistent severity emoji/badges and collapsible `<details>` sections for long content. (`plan.md` §13 Phase 7)

---

### Review Modes and Model Routing (FR-076–FR-085) — (V1)

**FR-076 — Six review modes**
*Acceptance criteria:* `quick`, `standard`, `deep`, `security`, `performance`, `tests` are each defined by token budget, model tier, prompt template, enabled categories, and neighbor-expansion depth. (`plan.md` §13 Phase 8)

**FR-077 — Complexity-based routing**
*Acceptance criteria:* a complexity score (`plan.md` §6 Stage 5 formula) selects model tier; `quick`/low-complexity → small/fast model, diff-only; `deep`/`security`/high-complexity/sensitive-path-touched → frontier model, full context.

**FR-078 — Escalation on high-severity cheap-pass findings**
*Acceptance criteria:* if a cheap pass returns ≥1 high/critical finding, only the affected chunks are re-run on the stronger model; both attempts are recorded in `llm_calls`.

**FR-079 — Budget enforcement at three levels**
*Acceptance criteria:* per-review, per-installation-per-day, and global daily USD caps degrade mode (deep→standard) rather than fail, recording `degraded_reason`.

**FR-080 — Slash-command mode selection**
*Acceptance criteria:* `/prrisk review --mode=<mode>` parses from `issue_comment`, checks commenter write-access, and reacts with an acknowledgment emoji.

**FR-081 — Config precedence**
*Acceptance criteria:* repo `.prrisk.yml` > installation settings > system defaults; invalid config produces a helpful error comment, not a silent fallback with no explanation.

**FR-082 — Second LLM provider support**
*Acceptance criteria:* a second provider implementation satisfies the same `Provider` interface and schema contract (parity-tested).

**FR-083 — Mode-specific prompt emphasis**
*Acceptance criteria:* `security` mode emphasizes authz/injection/secrets/crypto; other modes have analogous domain emphasis per `plan.md` §6 "Prompt structure".

**FR-084 — Global kill switch**
*Acceptance criteria:* an environment-level flag can immediately halt all LLM spend system-wide. (`plan.md` §10 "Abuse and cost")

**FR-085 — Per-installation daily cost visibility**
*Acceptance criteria:* the degradation reason and current spend are queryable/visible, feeding into the dashboard's cost views.

---

### Dashboard (FR-086–FR-095) — (V1)

**FR-086 — GitHub OAuth login**
*Acceptance criteria:* standard OAuth 2.0 authorization-code flow with `state` verification; session cookie created on success.

**FR-087 — Installation-scoped access**
*Acceptance criteria:* a logged-in user sees only installations they have access to via `user_installations`.

**FR-088 — Review list with filters and pagination**
*Acceptance criteria:* filterable by repo, status, band, mode, date range; cursor-based pagination, never offset-based.

**FR-089 — Review detail view**
*Acceptance criteria:* shows score, band, category breakdown (rendered from the stored `Contribution` list), findings, file impacts, and LLM cost/latency — sufficient to answer "why this score" without reading code.

**FR-090 — Re-run trigger**
*Acceptance criteria:* `POST /reviews/:id/rerun` enqueues a new review in the chosen mode; returns `409` if one is already in flight for that PR.

**FR-091 — Tenant isolation enforcement**
*Acceptance criteria:* a user requesting another installation's review receives `404` (never `403`, to avoid confirming existence); enforced at the store layer, not the handler.

**FR-092 — Installation settings management**
*Acceptance criteria:* default review mode, budget caps, and enabled categories are viewable and editable per installation, subject to the requester's role.

**FR-093 — Repo activation toggle and config override**
*Acceptance criteria:* an admin can deactivate a repo (stops future analysis) and view/override its parsed `.prrisk.yml`.

**FR-094 — Session security**
*Acceptance criteria:* cookies are `HttpOnly`, `Secure`, `SameSite=Lax`, short-lived, and revocable server-side.

**FR-095 — Live status updates (Nice to have)**
*Acceptance criteria:* an SSE stream of review status transitions is available on the review-detail page; absence of this feature does not block any other requirement.

---

### Analytics (FR-096–FR-100) — (V1)

**FR-096 — Overview metrics**
*Acceptance criteria:* review counts by band, average score, average latency, total cost, and findings by category over a date range.

**FR-097 — Cost breakdown**
*Acceptance criteria:* cost per day, per model, per repo, per mode.

**FR-098 — Risk hotspots**
*Acceptance criteria:* files ranked by cumulative `risk_contribution` and appearance frequency across reviews, linking to each file's review history.

**FR-099 — Aggregation performance**
*Acceptance criteria:* analytics queries return in a reasonable interactive time against seeded data (~50k reviews); a materialized view is introduced only if query time exceeds ~200ms. (`plan.md` §13 Phase 10)

**FR-100 — Answerable-in-10-seconds bar**
*Acceptance criteria:* "what did this cost last month," "which model is most expensive per review," "which files are chronic hotspots," and "is average risk trending up or down" are each answerable from the UI without a manual query. (`plan.md` §13 Phase 10 "Done when")

---

### Cost, Abuse Control, and Reliability (FR-101–FR-115)

**FR-101 — Rate limiting (MVP)**
*Acceptance criteria:* reviews per hour are capped per installation; requests per IP are capped on public endpoints. (`plan.md` §10 "Abuse and cost")

**FR-102 — Delete-on-uninstall (MVP)**
*Acceptance criteria:* `installation.deleted` triggers a data-retention deletion path for that installation's raw LLM responses/diffs per the retention policy (see PRD Assumption A5). (`plan.md` §10 "Data handling")

**FR-103 — Circuit breaker per LLM provider (V2)**
*Acceptance criteria:* repeated provider failures open the circuit; requests fail fast to a fallback provider or a signals-only degraded path; half-open probing resumes normal traffic once healthy. (`plan.md` §13 Phase 11)

**FR-104 — GitHub rate-limit awareness (MVP)**
*Acceptance criteria:* `x-ratelimit-remaining`/`x-ratelimit-reset` headers are read on every response; non-urgent calls are delayed when remaining is low; secondary-limit `retry-after` is honored. (`plan.md` §11)

**FR-105 — Dead-letter queue with replay (V2)**
*Acceptance criteria:* tasks exhausting retries land in a DLQ inspectable and replayable via a command or the dashboard. (`plan.md` §13 Phase 11)

**FR-106 — Full error classification (MVP)**
*Acceptance criteria:* every error at an external-call boundary is classified `Retryable`, `Terminal`, or `Degradable`, and handled accordingly (see `security.md`/`LLD.md` error tables). (`plan.md` §11)

**FR-107 — Degradation ladder (MVP)**
*Acceptance criteria:* the specific fallback sequence in `plan.md` §11 ("full analysis → standard mode → diff-only → partial chunks → signals-only → neutral check run with explanation") is implemented such that the system essentially never produces nothing.

**FR-108 — Load-tested concurrency limits (V2)**
*Acceptance criteria:* a documented `k6`/`vegeta` scenario simulates 100 concurrent PR webhooks; queue depth, worker saturation, and DB pool contention are measured and tuned from results. (`plan.md` §13 Phase 11)

**FR-109 — Chaos-tested recovery (V2)**
*Acceptance criteria:* worker-kill-mid-review, Redis-severed, and garbage-LLM-forever scenarios each recover or degrade correctly per documented expected behavior. (`plan.md` §13 Phase 11)

**FR-110 — Evaluation harness (V2, tracked from MVP)**
*Acceptance criteria:* `make eval` runs 25–40 labeled PRs and reports band accuracy, TP/FP rates, cost, latency, and cross-run variance, committed per prompt/scoring version. (`plan.md` §12, §13 Phase 12)

**FR-111 — `.prrisk.yml` schema validation (MVP for defaults; full precedence chain V1)**
*Acceptance criteria:* config schema is validated on load; malformed config falls back to safe defaults rather than blocking analysis.

**FR-112 — Config-driven sensitive path overrides (MVP)**
*Acceptance criteria:* a repo can extend/override the default sensitive-path glob list via `.prrisk.yml`.

**FR-113 — Retention enforcement (MVP)**
*Acceptance criteria:* raw LLM responses and full diffs are deleted/expired after the configured retention window; findings and scores are retained. (`plan.md` §10 "Data handling")

**FR-114 — Denylisted file exclusion regardless of diff (MVP)**
*Acceptance criteria:* `.env*`, `*.pem`, `*.key`, `secrets/**` are never sent to the LLM even if changed in the diff. (`plan.md` §10)

**FR-115 — No-training-tier provider selection (MVP, policy requirement)**
*Acceptance criteria:* the selected LLM provider/tier contractually does not train on submitted input; this is stated in the README/docs. (`plan.md` §10)

---

## Non-Functional Requirements

**NFR-001 — Webhook latency (MVP)**
Webhook acknowledgment completes in under 1 second under normal load. *Test:* load test against `/webhooks/github` measuring p99 response time.

**NFR-002 — Review turnaround (MVP)**
Standard-mode review completes in ~60 seconds end-to-end under normal conditions. *Test:* timed integration run via `cmd/replay` and against the test GitHub App.

**NFR-003 — Determinism (MVP)**
Identical inputs (diff + signals + LLM fixture) produce identical scores on every run. *Test:* `scoring` determinism test, 1000 iterations, byte-identical output.

**NFR-004 — Explainability (MVP)**
Every score is traceable to a stored, versioned breakdown answerable without reading code. *Test:* dashboard review-detail renders `Contribution[]` for every seeded review fixture.

**NFR-005 — Reproducibility (MVP)**
Any historical score can be recomputed offline from `signals` + `scoring_version`. *Test:* `cmd/replay` recompute test against archived review rows.

**NFR-006 — No PAT usage (MVP)**
No personal access token is used in any environment, including local development. *Test:* code review / static check for `PAT`/`GITHUB_TOKEN`-as-user-token patterns; App-only auth path enforced in `github` package.

**NFR-007 — Tenant isolation (MVP for API scoping; full test suite V1 w/ dashboard)**
No dashboard or API query can return data belonging to an installation the requester cannot access. *Test:* cross-installation access attempt expects `404`.

**NFR-008 — Secret hygiene in logs (MVP)**
No log line ever contains a diff, prompt, token, or secret value. *Test:* log-scrubbing unit tests asserting redaction on structured log fields.

**NFR-009 — Cost attribution completeness (MVP)**
Every LLM call, successful or failed, is recorded with token counts and computed cost. *Test:* assert `llm_calls` row count matches actual call count in integration tests, including induced failures.

**NFR-010 — No CI dependency on live external services (MVP)**
No test suite run hits a real LLM provider or real GitHub API. *Test:* CI configuration audit; all external calls mocked/stubbed/fixture-replayed.

**NFR-011 — Race-safety (MVP)**
The concurrent pipeline is free of data races. *Test:* `go test -race` across the full suite in CI.

**NFR-012 — Coverage bar (MVP)**
~70% overall coverage; near-100% on `scoring`, `diff`, and `findings` packages. *Test:* CI coverage report gated on these package thresholds.

**NFR-013 — Graceful degradation availability (MVP core path; full chaos suite V2)**
The system produces a completed (possibly degraded) review for every documented failure mode rather than an unterminated or failed review. *Test:* induced-failure integration tests per `plan.md` §11 degradation ladder.

**NFR-014 — Terminal check-run guarantee (MVP)**
No check run is ever left `in_progress` after a worker process exits, crashes, or times out. *Test:* worker-kill-mid-review recovery test (reaper) asserts terminal state within the reaper interval.

**NFR-015 — Cost predictability (MVP)**
Budget caps degrade review depth rather than allowing unbounded spend. *Test:* induced budget-exceeded test asserts mode downgrade and `degraded_reason`, not failure.

---

## Validation Requirements

- All GitHub webhook payloads are validated for required fields (`X-GitHub-Event`, `X-GitHub-Delivery`, JSON body) before routing; malformed payloads that pass signature verification but fail structural validation are logged and return `204`.
- All LLM findings are validated against the frozen JSON schema (`plan.md` §6 Stage 3) before entering the findings pipeline; schema-invalid entries are dropped (FR-045/046).
- All `.prrisk.yml` config is validated against a defined schema on load; invalid fields are rejected individually with a fallback to defaults for that field, not a full-file rejection, unless the file is unparseable YAML.
- All dashboard API inputs (filters, pagination cursors, mode selections on rerun) are validated server-side; invalid enum values (e.g., an unknown `mode`) return `400` with the standard error envelope (see [API.md](./API.md#standard-error-format)).

## Error Scenarios

| Scenario | Expected Behavior | Requirement |
|---|---|---|
| Webhook signature invalid | `401`, not persisted | FR-001 |
| Webhook redelivered | `200`, no-op | FR-002 |
| GitHub files endpoint truncates at 300 | `stats.files_truncated = true`, analysis continues | FR-021 |
| A file has no `patch` field (large/binary) | Treated as nil, not dereferenced — file excluded from diff-based context, not a crash | `plan.md` §11 "GitHub-specific handling" |
| LLM returns malformed JSON twice | Stage marked `invalid_json`, chunk tolerated as failed, other chunks proceed | FR-042 |
| LLM finding cites code not in supplied context | Dropped | FR-044 |
| All LLM calls fail for a review | Review completes in degraded, signals-only mode | FR-107 |
| Comment position rejected (`422`) | That finding demoted to summary; review still completes | FR-071 |
| Budget cap exceeded mid-review | Mode downgraded (e.g., deep→standard), `degraded_reason` recorded | FR-079 |
| Two workers dequeue the same task | Guarded `UPDATE ... WHERE status=...` ensures only one proceeds | FR-013 |
| New push arrives mid-analysis | Prior review superseded; publish aborts if status changed | FR-014, FR-073 |
| Worker crashes mid-review | Reaper marks it `failed` after timeout; check run still reaches terminal state | FR-015, NFR-014 |
| Cross-installation dashboard access attempt | `404` | FR-091, NFR-007 |
| PR content contains a prompt-injection attempt | Ignored — content is data, never instruction; no tool/network access to exploit | FR-053 |

## Edge Cases

- A PR renames a file with no content changes — the diff parser and position map must not error, and no finding should reference the pre-rename path in a way that fails anchoring silently. (FR-022, FR-023)
- A file has Windows CRLF line endings mixed with LF elsewhere in the repo. (FR-022)
- A file's last line has no trailing newline. (FR-022)
- A PR touches 200+ files, mixing source, generated, and lockfile changes — token budgeting must degrade to diff-only for lower-ranked files rather than failing outright. (FR-029)
- A PR is opened by a bot account — skipped by default with a recorded reason. (FR-005)
- A PR is force-pushed such that the head SHA changes but the PR number does not — the idempotency key changes (correctly triggering a new review) while the prior review is explicitly superseded, not orphaned. (FR-011, FR-014)
- An installation is suspended while a review is in flight — in-flight reviews should still reach a terminal state; new events for that installation are not processed until unsuspended.
- A repo's `.prrisk.yml` is syntactically valid YAML but references an undefined review mode — falls back to the default mode with a recorded warning, not a hard failure. (FR-111)
- Two pushes arrive within milliseconds of each other (rapid-fire `synchronize`) — exactly one review should end up live; all earlier ones superseded, none left `running` indefinitely. (FR-014)
