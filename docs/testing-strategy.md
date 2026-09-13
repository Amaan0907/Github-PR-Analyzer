# Testing Strategy — GitHub PR Risk Analyzer

**Derived from:** [requirements.md](./requirements.md) · **Source of truth:** [`../pr-risk-analyzer-plan.md`](../pr-risk-analyzer-plan.md) §12
**Related:** [LLD.md](./LLD.md) · [security.md](./security.md) · [development-plan.md](./development-plan.md)

Testing infrastructure is scoped to what `plan.md` §12 actually calls for — no additional frameworks or environments are introduced. The rule that governs every layer below: **no test hits a real LLM provider or real GitHub in CI** (`plan.md` §12 "Rules").

---

## Unit Tests

The majority of the suite, and the fastest to run.

**`scoring` (near-100% coverage target, NFR-012)**
- Table-driven tests over the full formula (see [LLD.md §4.5 "Risk scoring"](./LLD.md)): each category's saturation curve, the blast-radius multiplier's every component, both floors and the ceiling.
- Golden-file tests: ~20 fixture `ScoringInput`s covering every band, committed with expected `ScoringResult` output; a weight change must intentionally update goldens (FR-063).
- Property tests: monotonic in severity, additive in findings, output always in `[0,100]` (FR-065).
- Determinism test: identical input run 1000× produces byte-identical output (FR-064).

**`diff` (near-100% coverage target)**
- Parser fixtures: renames, multi-hunk files, additions at EOF, no-trailing-newline, CRLF, binary entries, empty files, huge files (FR-022).
- Position-map correctness against fixtures covering every line-type transition (FR-023); correctness additionally verified once against a real PR by posting one throwaway comment and confirming placement (manual, gated — see End-to-End below).
- Classification tests across source/test/config/migration/generated/vendored/lockfile/docs/binary (FR-024).

**`findings` (near-100% coverage target)**
- Validation order tests: each of the six checks in [LLD.md §4.3 "Finding validation order"](./LLD.md) tested independently and in combination.
- Evidence-hallucination test: plant a fabricated finding (evidence not present in supplied context) in a fixture response, assert it's dropped (FR-044).
- Dedupe and confidence-calibration unit tests, including every multiplier in [LLD.md §4.4 "Confidence calibration"](./LLD.md) applied singly and stacked.
- Cap enforcement: total (25) and per-file (5) limits, correct retention of highest severity × confidence (FR-050).

**`signals`**
- Sensitive-path glob matching, including per-repo `.prrisk.yml` overrides (FR-032).
- Dependency-diff classification across manifest formats (`go.mod`, `package.json`, `requirements.txt`, `pom.xml`), covering major/minor/patch and new/removed direct dependencies (FR-033).
- Test-ratio computation across Go/JS/Python test-file conventions (FR-034).

**`llm`**
- Response parsing against recorded fixtures, including malformed ones: fenced JSON, trailing prose, truncated output, wrong enum values (FR-042).
- One-repair-then-fail behavior verified explicitly — never zero repairs, never unlimited (FR-042).
- Cost calculation correctness from token counts and pricing tables.

**Config**
- `.prrisk.yml` schema validation: valid file, invalid individual field (falls back to default for that field), unparseable YAML (falls back to full defaults with a recorded warning) (FR-111).

---

## Integration Tests

Run against real Postgres and Redis via testcontainers — the layer where component interactions, not pure logic, are the point.

- **Migrations up and down cleanly** against a fresh database (`plan.md` §12).
- **Full worker pipeline**, stubbed LLM provider (`testdata/llm_responses/`) and stubbed GitHub client, driven by saved webhook payloads from `testdata/webhooks/` — exercises the entire analyzer orchestration without any network call.
- **Idempotency:** fire the same webhook 5× concurrently; assert exactly one `reviews` row and one analysis run (FR-011, FR-012).
- **Supersession ordering:** fire `opened` then `synchronize` in sequence; assert the first review ends `superseded` and nothing is posted for it (FR-014).
- **Guarded state transitions under race:** two simulated workers attempt to claim the same task; assert exactly one wins the `queued → running` transition (FR-013).
- **Stale-review reaping:** seed a `running` review past its timeout; assert the reaper marks it `failed` with `error_code = STALE` (FR-015).
- **Failure-path completion:** LLM always errors → review still completes in degraded, signals-only mode with a `neutral` check run, not a stuck or failed pipeline (FR-107, NFR-013).

---

## API Tests

- **Webhook signature verification:** valid, invalid, missing header, tampered body, oversized body — each asserted against the exact expected status code (`401`/`202`/`200`/`413`) (FR-001, FR-006).
- **GitHub client** against `httptest` servers replaying recorded responses: pagination across the 300-file cap, `404`, `403` + `retry-after` honored, truncated file lists, nil `patch` field handled without panic (FR-021, `plan.md` §11 "GitHub-specific handling").
- **Dashboard handlers** with a stub store: standard error-envelope shape verified on every error path; tenant scoping verified by attempting cross-installation access and expecting `404` (FR-091).
- **Rerun endpoint:** `409 REVIEW_IN_FLIGHT` when a review is already queued/running for the target PR; `202` with a new review ID otherwise (FR-090).
- **Pagination:** cursor correctness across multiple pages, including the boundary case of an empty final page (`next_cursor: null`).

---

## Database Tests

Covered primarily under Integration Tests above; specific assertions worth calling out on their own:

- The partial unique index on `reviews(pull_request_id, head_sha, review_mode) WHERE status <> 'superseded'` actually prevents a second live insert under concurrent conditions, independent of the queue-level guard (FR-012).
- `ON DELETE CASCADE` from `reviews` correctly removes `findings`, `file_impacts`, `llm_calls` when a review row is deleted (exercised only in test fixtures — production code path never deletes a `reviews` row per the retention policy, see [database-design.md §8](./database-design.md#8-data-lifecycle-and-retention)).
- Tenant-scoped query helpers refuse to compile/construct without an `installationID` argument (a compile-time or lint-level check, not just a runtime test, where the language allows it).

---

## External API Mocking

- **GitHub:** all tests use `httptest` servers replaying recorded response fixtures, or the stub `github.Client` used by `cmd/replay`. No live GitHub App/org is touched outside the manual End-to-End gate below.
- **LLM provider:** `llm.StubProvider` replays `testdata/llm_responses/` — this is the only provider implementation exercised in CI (FR-054).
- Recorded fixtures are versioned alongside the prompt/schema version they correspond to, so a schema change that breaks old fixtures is caught by the suite itself, not discovered later.

---

## Authentication Tests

- OAuth flow: successful code exchange creates a session; `state` mismatch is rejected before any token exchange is attempted; missing `code` returns `400`.
- Session validity: expired/tampered session cookie is rejected, not silently treated as anonymous-with-no-installations.
- App-to-GitHub auth: JWT minting produces a token with the correct `iss`/expiry; installation-token caching honors TTL and refreshes correctly on a simulated `401`.
- **No test constructs or relies on a PAT** — consistent with NFR-006, this is enforced by never having a PAT code path to test in the first place.

---

## Business Logic Tests

This is largely the union of the `scoring`, `findings`, and `signals` unit-test sections above, called out separately here because it's the layer with the highest interview/portfolio value (`plan.md` §19):

- Scoring is provably deterministic, monotonic, and explainable (see `scoring` unit tests).
- Confidence calibration is provably deterministic and never simply passes through model self-reported confidence (FR-047).
- The evidence-verification hallucination filter is provably effective against a planted fabricated finding (FR-044).
- Blast-radius contributions fire independently of whether the LLM found anything, verified with a fixture that has zero findings but a large, untested, sensitive-path change, asserting a non-trivial score (`plan.md` §7 "Layer B").

---

## Error-Path Tests

Directly exercising the classification table in [LLD.md §6](./LLD.md#6-error-handling):

| Induced condition | Expected outcome | Requirement |
|---|---|---|
| GitHub 5xx on diff fetch | Retried with backoff, succeeds on a later attempt in the fixture | FR-017 |
| GitHub 404 (PR deleted mid-analysis) | Review fails immediately, not retried | FR-106 |
| GitHub 422 on one inline comment | That comment demoted to summary, others post, review completes | FR-071 |
| LLM timeout on every chunk | Review completes signals-only, `degraded_reason` set | FR-107 |
| LLM returns malformed JSON twice for one chunk | Chunk marked failed, other chunks unaffected | FR-042 |
| Budget cap exceeded mid-review | Mode downgraded, `degraded_reason` recorded, review still completes | FR-079 |
| Worker panics mid-pipeline | Recovered, review marked failed, worker process survives, no retry | FR-019 |
| Two workers race the same task | Exactly one transitions it; the other observes zero rows affected and exits cleanly | FR-013 |

---

## Security Tests

Directly exercising the high-risk areas flagged in [security.md](./security.md#high-risk-areas-summary):

- Webhook signature: valid, invalid, missing, tampered-body-with-valid-header-format cases all produce the correct rejection.
- Cross-tenant access attempt on every tenant-scoped endpoint returns `404`, never a data leak, never a distinguishing `403` (FR-091, NFR-007).
- Secret redaction: planted fake secrets (API-key-shaped strings, a fake PEM block) in a fixture diff are verified redacted before the context bundle is considered "built" — asserted on the `context.Bundle` output directly, not just on the final prompt string (FR-028).
- Denylisted files (`.env`, `*.pem`) are excluded from context even when present in the diff (FR-114).
- Prompt-injection fixture: a diff containing an embedded "ignore previous instructions" string produces no behavioral change in what gets posted (the injected text is either ignored or, if surfaced as a "finding," fails evidence verification and is dropped) (FR-053).
- Session cookie attributes (`HttpOnly`, `Secure`, `SameSite=Lax`) asserted on every `Set-Cookie` response from the auth endpoints (FR-094).

---

## Frontend Tests

Scoped to the dashboard (V1), per `plan.md` §13 Phase 9:

- Component tests (Vitest + Testing Library) with mocked API responses: reviews list (filters, pagination, empty/loading states), review detail (score breakdown rendering from a fixture `Contribution[]`), findings list grouped by category.
- Accessibility pass on keyboard navigation and contrast for the core dashboard views (`plan.md` §13 Phase 9 "Testing").

---

## End-to-End Tests

- **Automated (Playwright):** one happy-path flow — login → installation list → review list → review detail — against a stub/mocked API layer (`plan.md` §13 Phase 9).
- **Manual, gated (run before each release):** a test GitHub App installed on a scratch repo in a test org, with a seeded PR set covering: a trivial typo fix, a large refactor, a deliberate SQL injection, an auth change with no tests, a dependency major-version bump, and a PR touching 200 files. Comment placement is verified **visually** — `plan.md` §12 is explicit that this is "the one thing automated tests can't fully confirm."
- The position-map correctness check (FR-023) is folded into this manual gate: at least one real posted comment is visually confirmed to land on the intended line before Phase 3/7 are considered done.

---

## Evaluation Harness (distinct from conventional testing)

Not a test suite in the pass/fail sense — a measurement harness, called out separately because `plan.md` §19 identifies it as the project's single highest differentiator:

- 25–40 real, labeled PRs from public repos, each with an expected risk band and known issues.
- `make eval` reports: band accuracy (within ±1 band), true-positive rate on known issues, false-positive rate on trivial PRs, average cost, average latency, and score variance across 3 runs of the same PR.
- Results are committed per prompt version and per scoring version in a CSV, enabling a claim like "prompt v3 cut false positives 34% while holding recall" (`plan.md` §12, §19 item 5).

---

## Coverage Targets (NFR-012)

| Package | Target |
|---|---|
| `scoring` | Near-100% |
| `diff` | Near-100% |
| `findings` | Near-100% |
| Overall repository | ~70% |

`-race` is run on the entire suite in CI, given the pipeline's concurrency (bounded goroutines in context-fetching and chunk analysis) — a data race here would silently corrupt scoring inputs (NFR-011).

## What Is Deliberately Not Built

Per the instruction to avoid excessive testing infrastructure not justified by the project: no dedicated mutation-testing framework, no separate contract-testing service (Pact, etc.) for the GitHub/LLM boundaries — `httptest` fixtures and the stub provider already serve that purpose at the scale this project operates. No cross-browser matrix for the dashboard beyond what Playwright's default configuration covers — this is an internal engineering dashboard, not a consumer product with a broad device/browser support matrix requirement in `plan.md`.
