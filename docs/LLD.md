# Low-Level Design — GitHub PR Risk Analyzer

**Derived from:** [HLD.md](./HLD.md) · **Source of truth:** [`../pr-risk-analyzer-plan.md`](../pr-risk-analyzer-plan.md) §2, §6, §7, §8, §9, §11
**Related:** [database-design.md](./database-design.md) · [API.md](./API.md) · [security.md](./security.md) · [testing-strategy.md](./testing-strategy.md)

This document specifies internal structure only — how the components named in [HLD.md](./HLD.md) are actually organized as Go packages, their interfaces, and the algorithms that would otherwise be ambiguous from the architecture alone. No implementation code; interfaces and pseudocode only, per `plan.md` §9 (repository structure) and §16 (development-order constraints).

---

## 1. Package/Module Structure

Single Go module, two binaries, dependency direction flows strictly downward — `scoring` and `diff` are leaves that import nothing internal (`plan.md` §9):

```
cmd/
  api/main.go
  worker/main.go
  replay/main.go            # dev tool: run the pipeline against a saved fixture, no network

internal/
  config/                   # env loading, validation, typed config struct
  httpapi/
    router.go
    middleware/              # requestid, logging, recovery, auth, ratelimit, cors
    webhook/                 # signature verify, event routing
    handlers/                # reviews, repos, analytics, auth
  github/
    app.go                   # JWT + installation token cache
    client.go                # go-github wrapper: pagination, retry, rate limits
    checks.go
    comments.go
    ratelimit.go
  diff/                      # parser, position mapper, file classifier — imports nothing internal
  context/                   # file selection, neighbor resolution, token budgeting, redaction
  signals/
    heuristics.go            # size, churn, sensitive paths, test ratio
    deps.go                  # manifest/lockfile diffing
    secrets.go                # entropy + pattern detection
    static/                   # optional sandboxed analyzer runners
  llm/
    provider.go               # interface
    gemini/ openai/ anthropic/ # one concrete impl for MVP, more later
    router.go                 # complexity -> model
    schema/                    # finding JSON schema + validation
    prompts/                   # versioned .tmpl files, embedded
    budget.go
  findings/                   # validate, anchor, dedupe, calibrate, cap
  scoring/                    # PURE — imports stdlib only
  analyzer/                   # pipeline orchestration
  publisher/                  # check runs, summary upsert, inline comments, dry-run
  queue/                       # task defs, payloads, enqueue helpers
  worker/                       # task handlers, state machine, supersession
  store/                        # postgres repos, tx helper, tenant scoping
  observability/                # logger, metrics, tracing
```

Package responsibility table matches [HLD.md §3](./HLD.md#3-major-components) one-to-one; nothing here introduces a component not already named there.

---

## 2. Core Interfaces

### 2.1 LLM Provider

```go
package llm

type Provider interface {
    Analyze(ctx context.Context, req AnalyzeRequest) (AnalyzeResponse, Usage, error)
}

type AnalyzeRequest struct {
    SystemPrompt string
    UserPrompt   string
    Schema       json.RawMessage // frozen finding schema, see §4.2
    Temperature  float32         // pinned to 0 (plan.md §6 Stage 3)
    Model        string          // exact pinned version, never a floating alias
}

type AnalyzeResponse struct {
    RawJSON  json.RawMessage
    Findings []RawFinding
}

type Usage struct {
    PromptTokens, CompletionTokens, CachedTokens int
    LatencyMs int
    CostUSD   float64
}
```

A `StubProvider` implementing the same interface replays `testdata/llm_responses/` fixtures — this is what `cmd/replay` uses, and what every pipeline unit test depends on instead of a live call (FR-054, `plan.md` §16 item 5).

### 2.2 Diff Engine

```go
package diff

type File struct {
    Path, OldPath  string          // OldPath set on rename
    ChangeType     ChangeType      // added|modified|removed|renamed
    Hunks          []Hunk
    Classification FileClass       // source|test|config|migration|generated|vendored|lockfile|docs|binary
    Binary         bool
}

type Hunk struct {
    OldStart, OldLines, NewStart, NewLines int
    Lines []Line
}

type Line struct {
    Type    LineType // context|added|removed
    NewLine int      // -1 if not present in new file
    OldLine int      // -1 if not present in old file
    Content string
}

// PositionMap resolves (file path, new-file line) -> GitHub diff `position`.
type PositionMap map[string]map[int]int

func Parse(rawDiff []byte) ([]File, error)
func BuildPositionMap(files []File) PositionMap
func Classify(f *File, gitattributes GitAttributes) FileClass
```

### 2.3 Context Builder

```go
package context

type Bundle struct {
    Files       []ContextFile   // full-content or diff-only per budget decision
    Truncated   bool
    TokenBudget int
    TokensUsed  int
}

type ContextFile struct {
    Path       string
    FullContent string // empty if diff-only
    DiffOnly    bool
    Rank        float64 // changed_lines x sensitivity
}

func Build(ctx context.Context, in BuildInput) (Bundle, error)
```

### 2.4 Signals

```go
package signals

type Signals struct {
    ChangedLines, FilesChanged int
    Languages                 []string
    ChangedLinesPercentile    float64  // vs repo history
    TestRatio                 float64
    MigrationPresent          bool
    SensitivePathsTouched     []SensitivityHit
    DependencyChanges         []DependencyChange
    TopDecileChurnTouched     bool
    MaxFanIn                  int
}

func Compute(ctx context.Context, in ComputeInput) (Signals, []findings.RawFinding, error)
// returns both the signal values AND any deterministic findings (test_gap, secret, etc.)
```

### 2.5 Findings

```go
package findings

type RawFinding struct {
    Category, Severity, Title, Description string
    Confidence                              float64 // model- or heuristic-reported
    FilePath                                 string
    StartLine, EndLine                       int
    Evidence, Suggestion                     string
    Source                                    string // llm|static_analysis|heuristic|dependency_scan
}

type Finding struct {
    RawFinding
    FinalConfidence float64
    Anchored        bool
    DiffPosition    *int
    DedupeHash      string
}

func Validate(raw []RawFinding, vctx ValidationContext) (kept []Finding, dropped []DroppedFinding)
func Dedupe(in []Finding) []Finding
func Calibrate(in []Finding, corroboration CorroborationIndex) []Finding
func Cap(in []Finding, maxTotal, maxPerFile int) []Finding
```

### 2.6 Scoring — pure, stdlib only

```go
package scoring

type ScoringInput struct {
    Findings []FindingSummary // category, severity, final_confidence only
    Signals  signals.Signals
    Stats    Stats
    Weights  WeightConfig // versioned constants, never scattered literals
}

type ScoringResult struct {
    Score          int
    Band           Band
    CategoryScores map[string]float64
    Blast          float64
    Breakdown      []Contribution
    ScoringVersion string
}

func Score(in ScoringInput) ScoringResult // zero I/O, zero non-stdlib imports
```

### 2.7 Publisher

```go
package publisher

type Publisher interface {
    UpsertCheckRun(ctx context.Context, review Review) error
    UpsertSummaryComment(ctx context.Context, review Review, summaryMD string) error
    PostInlineComments(ctx context.Context, review Review, findings []findings.Finding) (posted int, err error)
}

// DryRunPublisher implements Publisher and logs intended payloads instead of calling GitHub (FR-072).
```

### 2.8 Store (tenant-scoped repository layer)

```go
package store

type ReviewStore interface {
    // Every read method that returns tenant data requires installationID explicitly —
    // there is no method signature that allows omitting it.
    Get(ctx context.Context, installationID, reviewID uuid.UUID) (Review, error)
    List(ctx context.Context, installationID uuid.UUID, filter ReviewFilter) ([]ReviewSummary, string, error)
    TransitionStatus(ctx context.Context, reviewID uuid.UUID, from, to Status) (bool, error) // guarded UPDATE
    Persist(ctx context.Context, result PipelineResult) error // single transaction: review + findings + impacts + llm_calls
}
```

---

## 3. Handler → Service → Repository Flow

### 3.1 API service (synchronous request path)

```
HTTP request
  → middleware chain (requestid, logging, recovery, auth, ratelimit, cors)
  → handler (internal/httpapi/handlers)
      - decodes/validates request
      - resolves installationID from session (dashboard) or none (webhook)
  → store (internal/store)
      - executes tenant-scoped query/transaction
  → handler serializes response
```

Concretely for `GET /api/v1/reviews/:reviewId`:
```
handlers.GetReview(c *gin.Context)
  → sessionUser := middleware.UserFromContext(c)
  → installationIDs := sessionUser.AccessibleInstallations()
  → review, err := store.Reviews.Get(ctx, installationIDs, reviewID)
      // store method itself requires an installation scope argument; a handler
      // cannot construct a query that omits it (plan.md §10 "Tenancy and access")
  → if err == store.ErrNotFound { c.JSON(404, ...) ; return }
  → c.JSON(200, toReviewDetailDTO(review))
```

### 3.2 Worker service (asynchronous pipeline path)

```
asynq task dequeued
  → worker.Handler (internal/worker)
      - loads review row, checks guarded transition (queued -> running)
  → analyzer.Pipeline (internal/analyzer) — orchestrates, in order:
      → github.Client.GetDiff / GetFiles
      → diff.Parse, diff.Classify, diff.BuildPositionMap
      → signals.Compute
      → context.Build
      → llm.Router.SelectModel → llm.Provider.Analyze (map) → llm.Provider.Analyze (reduce)
      → findings.Validate → findings.Dedupe → findings.Calibrate → findings.Cap
      → scoring.Score
  → store.Reviews.Persist (single transaction)
  → queue.Enqueue(review:publish)
```

Then, for the publish task:
```
worker.PublishHandler
  → store.Reviews.Get (re-check status != superseded)
  → publisher.UpsertCheckRun
  → publisher.UpsertSummaryComment
  → publisher.PostInlineComments
  → store.Findings.MarkPosted
```

The **orchestrator (`analyzer.Pipeline`) is the only component that knows the stage order**; every individual package (`diff`, `signals`, `llm`, `findings`, `scoring`) is independently unit-testable with no knowledge of its neighbors — this mirrors the HLD's component-responsibility boundaries exactly.

---

## 4. Key Algorithms

### 4.1 Diff position mapping (FR-023)

```
for each file in parsed_diff:
    running_position = 0
    for each hunk in file.hunks:
        running_position += 1                # hunk header counts as one position
        new_line_cursor = hunk.new_start
        for each line in hunk.lines:
            running_position += 1
            if line.type in {context, added}:
                map[file.path][new_line_cursor] = running_position
                new_line_cursor++
            else:  # removed
                pass  # no new_line entry; old-side only, not commentable via `line`
```
Renames are keyed by `file.Path` (new identity); a finding reported against `OldPath` fails file-existence validation (§4.3 check 2) rather than silently mismapping. CRLF and no-trailing-newline files are normalized before parsing.

### 4.2 Frozen finding JSON schema (FR-041)

```jsonc
{
  "findings": [{
    "category": "bug|security|performance|architecture|test_gap",
    "severity": "info|low|medium|high|critical",
    "confidence": 0.0,
    "title": "string, <=90 chars",
    "description": "markdown, <=800 chars, concrete failure mode",
    "file_path": "path/as/in/diff",
    "start_line": 0, "end_line": 0,
    "evidence": "exact source line(s) copied from provided context",
    "suggestion": "markdown or null"
  }]
}
```
This is written once, before any prompt (`plan.md` §16 item 1); every provider is coerced into it via structured-output/JSON-mode/tool-calling, never free-form prose parsing.

### 4.3 Finding validation order (FR-044–FR-049)

Checks run in this order; the first failing check determines the outcome:
```
1. category/severity/confidence enum validity           -> fail: DROP
2. file_path in this review's changed-file set           -> fail: DROP
3. evidence is an exact/near-exact substring of the
   file content actually sent to the model                -> fail: DROP  (hallucination filter)
4. start_line within an added/modified hunk                -> fail: KEEP, anchored=false
5. dedupe_hash collision with an existing kept finding      -> MERGE, keep higher severity
6. category == style AND mode doesn't opt in                -> DROP
```

### 4.4 Confidence calibration (FR-047)

```
final_confidence = model_confidence
    × 0.85  if evidence matched loosely (normalized whitespace) rather than byte-exact
    × 1.10  if a deterministic signal corroborates (same file+line hit)
    × 0.80  if the file was diff-only (truncated context)
    × 0.75  if cross-file but only one side was in context
clamp to [0.05, 0.95]
```

### 4.5 Risk scoring (FR-056–FR-061)

```
weight(severity) = { critical: 40, high: 22, medium: 9, low: 3, info: 0.5 }

raw_c   = Σ weight(f.severity) × f.final_confidence   for f in findings where f.category == c
score_c = 100 × (1 − exp(−raw_c / K_c))               # K_c: versioned per-category constant

blast = 1.0
  + 0.15 sensitive_paths_touched
  + 0.10 migration_present
  + 0.10 dependency_manifest_changed (weighted by major-bump count)
  + 0.10 changed_lines > repo_p90
  + 0.10 test_ratio < 0.1 AND source_lines_changed > 50
  + 0.05 top_decile_churn_touched
  + 0.05 fan_in > threshold
  blast = min(blast, 1.6)

base  = Σ w_c × score_c     # w: security .30 bug .30 architecture .15 performance .15 tests .10
score = round(min(100, base × blast))

# floors/ceilings, applied last, in order:
if ∃ finding: severity=critical, confidence≥0.8:            score = max(score, 75)
if ∃ finding: severity=high, category=security, conf≥0.8:   score = max(score, 60)
if findings.empty AND blast≤1.1 AND changed_lines<30:        score = min(score, 15)

band = 0-24 low | 25-49 moderate | 50-74 high | 75-100 critical
```
Every non-zero term above emits a `Contribution{Label, Value, Weight}` entry, which is exactly what the dashboard's "why this score" view renders (FR-061, FR-089).

### 4.6 Complexity score and model routing (FR-077, FR-078)

```
complexity = 2·log1p(changed_lines) + 3·files_changed + 5·distinct_languages
           + 15·sensitive_path_hits + 10·dependency_changes + 8·cross_module_edits

route:
  mode=quick OR complexity<20                  -> small/fast model, diff-only, no synthesis
  mode∈{deep,security} OR complexity>70
    OR sensitive_paths_touched                 -> frontier model, full context, wide neighbors
  cheap pass returned ≥1 high/critical finding -> escalate: re-run affected chunks on stronger model
  default                                       -> mid-tier model, map+reduce
```

### 4.7 Idempotency key and supersession (FR-011, FR-014)

```
idempotency_key = sha256(installation_id | repo_id | pr_number | head_sha | mode)

on pull_request.synchronize:
  prior_ids = UPDATE reviews SET status='superseded'
              WHERE pull_request_id=? AND status IN ('queued','running')
              RETURNING id
  for id in prior_ids: redis.set("cancel:"+id, "1", ttl=1h)
  enqueue(review:analyze, idempotency_key(new_head_sha))
```
The worker checks `redis["cancel:"+review_id]` only at pipeline checkpoints (never mid external call) — see [HLD.md §5.2](./HLD.md#52-analysis-asynchronous-target-60s-standard-mode) for the checkpoint list.

---

## 5. Validation

- **Webhook payload validation:** structural check (required headers, parseable JSON) after signature verification, before routing.
- **Config validation (`.prrisk.yml`):** schema-validated on load; a field-level error falls back to the default for that field rather than rejecting the whole file (FR-111).
- **API input validation:** enum fields (`status`, `band`, `mode`) validated against fixed sets at the handler boundary before reaching the store layer; invalid values short-circuit with `400` before any query executes.
- **Finding schema validation:** see §4.2/§4.3 — this is the highest-value validation path in the system per `plan.md` §19 item 2.

## 6. Error Handling

```go
package pipelineerr

type Class int
const (Retryable Class = iota; Terminal; Degradable)

type Error struct {
    Code  string
    Class Class
    Stage string
    Err   error
}
```

Classification happens exactly once, at the boundary of each external call (`github.Client`, `llm.Provider`); internal packages never re-classify an error they didn't originate. Full table in [security.md](./security.md) is out of scope for that doc — the authoritative table lives here:

| Source | Example | Class | Handling |
|---|---|---|---|
| GitHub | 5xx, secondary rate limit | Retryable | backoff, honor `retry-after` |
| GitHub | 404 (PR deleted), 403 (uninstalled) | Terminal | fail review, no retry |
| GitHub | 422 (bad comment position) | Terminal (scoped to that comment) | drop comment, fold into summary |
| LLM | 429, timeout, 5xx | Retryable | backoff; drop chunk after max attempts (Degradable at pipeline level) |
| LLM | malformed JSON after 1 repair | Terminal (for that stage) | pipeline continues in Degradable mode |
| Budget | cap hit at any of 3 levels | Degradable | narrow mode, record `degraded_reason` |
| DB/Redis | connection blip | Retryable | standard backoff |
| Worker | panic | Terminal | `recover()`, mark failed, alert, no retry |

## 7. Configuration

```go
package config

type Config struct {
    DatabaseURL   string `env:"DATABASE_URL,required"`
    RedisURL      string `env:"REDIS_URL,required"`
    GitHubAppID   string `env:"GITHUB_APP_ID,required"`
    GitHubPrivateKeyB64 string `env:"GITHUB_PRIVATE_KEY_B64,required"` // base64 PEM, survives env mangling
    WebhookSecret string `env:"GITHUB_WEBHOOK_SECRET,required"`
    LLMProvider   string `env:"LLM_PROVIDER,required"`
    LLMAPIKey     string `env:"LLM_API_KEY,required"`
    DryRun        bool   `env:"PRRISK_DRY_RUN" default:"false"`
    GlobalKillSwitch bool `env:"PRRISK_KILL_SWITCH" default:"false"`
}
```
Fails loudly (process exits non-zero with a clear message) on any missing required variable at startup — never a partial start with a nil dependency (`plan.md` §13 Phase 0).

## 8. Logging

- Structured JSON via `log/slog`, fields: `request_id`, `review_id`, `installation_id`, `stage`.
- Never logs: diffs, prompts, tokens, secrets, installation access tokens (`plan.md` §10, §11).
- The invalid-JSON rate and evidence-rejection rate are logged as named metrics specifically because they are the model-quality canaries called out in `plan.md` §11.

---

## 9. Queue Task Definitions (reference)

| Task | Payload | Queue | Timeout | Retries | Unique key |
|---|---|---|---|---|---|
| `review:analyze` | `{review_id, installation_id, repo_id, pr_number, head_sha, mode}` | critical/default | 8 min | 3 | idempotency key (§4.7) |
| `review:publish` | `{review_id}` | default | 2 min | 5 | `review_id` |
| `repo:sync_config` | `{repo_id, config_sha}` | low | 1 min | 3 | `repo_id\|config_sha` |
| `repo:compute_file_stats` | `{repo_id}` | low | 5 min | 2 | `repo_id` (24h) |
| `maintenance:reap_stale` | `{}` | scheduled/5min | — | — | n/a |

## 10. Concurrency Model

- File fetches and LLM chunk analysis run under `errgroup.Group` + `semaphore.Weighted` (default 8 concurrent GitHub calls, 4 concurrent LLM calls per review).
- Each pipeline stage runs under its own `context.WithTimeout` derived from the task deadline.
- Worker replica concurrency defaults to 5 (bottleneck is external latency, not CPU).
- The only cross-goroutine coordination point is the Redis cancellation-flag read, which is read-only from the worker's perspective and requires no locking.
