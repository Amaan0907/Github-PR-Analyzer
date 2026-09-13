# API Design — GitHub PR Risk Analyzer

**Derived from:** [HLD.md](./HLD.md) · **Source of truth:** [`../pr-risk-analyzer-plan.md`](../pr-risk-analyzer-plan.md) §4
**Related:** [database-design.md](./database-design.md) · [security.md](./security.md) · [requirements.md](./requirements.md)

Every endpoint below appears in `plan.md` §4. No endpoint has been added beyond what the plan specifies; anything marked **(V1)** is deferred per the plan's own phasing (Phase 8–10) rather than invented.

---

## 1. Conventions

### Versioning
All dashboard-facing and auth endpoints are prefixed `/api/v1`. GitHub-facing endpoints (`/webhooks/github`, `/healthz`, `/readyz`, `/metrics`) are unversioned infrastructure endpoints, not part of the public API surface. (`plan.md` §4)

### Pagination
Cursor-based only, never offset-based, on any list endpoint (`reviews`, `findings`). Query params: `?cursor=<opaque>&limit=<int, default 20, max 100>`. Response includes `next_cursor` (`null` when exhausted). (`plan.md` §4 "Conventions")

### Filtering
List endpoints accept filter query params specific to the resource (see per-endpoint tables below) — repo, status, band, mode, date range for reviews; category, severity, min confidence for findings.

### Sorting
Reviews and findings are returned newest-first (`created_at DESC`) by default; no client-specified sort field is exposed in the MVP surface (not required by `plan.md`).

### Standard Error Format
```json
{
  "error": {
    "code": "REVIEW_IN_FLIGHT",
    "message": "A review for this PR is already in progress.",
    "request_id": "01HXYZ..."
  }
}
```
`X-Request-ID` is present on every response and propagates into logs and worker task payloads for cross-service tracing. (`plan.md` §4 "Conventions")

### Rate Limiting
- Per-installation: reviews-per-hour cap (manual triggers via `POST /reviews/:id/rerun` and slash commands count against it).
- Per-IP: request cap on public endpoints (`/webhooks/github`, `/auth/*`).
- Exceeding either returns `429` with the standard error envelope (`code: RATE_LIMITED`). (`plan.md` §10 "Abuse and cost")

### Idempotency
- `POST /webhooks/github` is idempotent on `X-GitHub-Delivery` (§5.2 below).
- `POST /reviews/:reviewId/rerun` is not idempotent by header, but is protected by the same `(pr, head_sha, mode)` uniqueness as any other trigger — a duplicate rerun request for an in-flight combination returns `409`, not a second review. (`plan.md` §4, §5 step 8)

---

## 2. Public / GitHub-Facing Endpoints

### `POST /webhooks/github`
**Purpose:** receive and process GitHub App webhook deliveries.
**Auth:** none (unauthenticated endpoint); integrity is enforced via HMAC signature, not a bearer credential.
**Authorization:** N/A.

| Header | Required | Purpose |
|---|---|---|
| `X-Hub-Signature-256` | Yes | HMAC-SHA256 of raw body, verified via `hmac.Equal` |
| `X-GitHub-Event` | Yes | Event type for routing |
| `X-GitHub-Delivery` | Yes | Dedup key |

**Request body:** raw GitHub webhook JSON payload (schema varies by event type; not redefined here — see GitHub's webhook documentation for payload shape, and `plan.md` §5 for which events/actions are handled).

**Responses:**
| Status | Meaning |
|---|---|
| `202` | Signature valid, event routed, new work enqueued |
| `200` | Signature valid, but a no-op (duplicate delivery, uninteresting event/action, or idempotent conflict) |
| `401` | Missing or invalid signature |
| `413` | Body exceeds the configured size limit |

**Validation rules:** raw body read before any JSON decode; signature checked before persistence; delivery ID checked before routing (FR-001, FR-002, FR-006).

### `GET /healthz`
**Purpose:** liveness probe. **Auth:** none. **Response:** `200 {"status":"ok"}` if the process is up.

### `GET /readyz`
**Purpose:** readiness probe — checks DB and Redis connectivity. **Auth:** none.
**Response:** `200 {"status":"ready","db":"ok","redis":"ok"}` or `503` with the failing dependency named.

### `GET /metrics`
**Purpose:** Prometheus scrape endpoint. **Auth:** none (assumed network-restricted to the scraping infrastructure, not public-internet-exposed — standard Prometheus deployment practice, not specified further in `plan.md`).

---

## 3. Auth Endpoints

### `GET /auth/github/login`
**Purpose:** initiate the dashboard OAuth flow. **Auth:** none.
**System action:** generates a signed `state` value, redirects to GitHub's OAuth authorize URL.
**Response:** `302` redirect.

### `GET /auth/github/callback`
**Purpose:** complete the OAuth flow. **Auth:** none (this endpoint establishes auth).
**Parameters (query):** `code` (string, required), `state` (string, required).
**System action:** verifies `state`, exchanges `code` for a GitHub access token, resolves the GitHub user, upserts `users`/`user_installations`, issues a session cookie.
**Responses:**
| Status | Meaning |
|---|---|
| `302` | Success — redirect to dashboard, `Set-Cookie` session |
| `400` | Missing/invalid `state` (CSRF check failed) or missing `code` |
| `502` | GitHub token exchange failed |

### `POST /auth/logout`
**Purpose:** end the session. **Auth:** session cookie required. **Response:** `204`, clears the session cookie.

### `GET /api/v1/me`
**Purpose:** current user identity plus accessible installations. **Auth:** session cookie required.
**Response `200`:**
```json
{
  "user": { "id": "uuid", "login": "string", "avatar_url": "string" },
  "installations": [ { "id": "uuid", "account_login": "string", "role": "string" } ]
}
```
**Errors:** `401` if not authenticated.

---

## 4. Dashboard API (all require session, all tenant-scoped) — (V1)

All endpoints below require a valid session cookie and resolve `installation_id` against `user_installations` for the requesting user before touching any domain data (FR-091, NFR-007). A resource belonging to an installation the user cannot access returns `404`, never `403` (to avoid confirming existence).

### `GET /api/v1/installations`
**Purpose:** list installations accessible to the current user.
**Auth:** session. **Authorization:** filtered to `user_installations` for this user.
**Response `200`:** `{ "data": [Installation] }`.

### `GET /api/v1/installations/:id/settings`
**Purpose:** read an installation's settings (default mode, budget caps, enabled categories).
**Auth:** session. **Authorization:** requester must have access to `:id`.
**Response `200`:** `{ "settings": { "default_mode": "standard", "budget_daily_usd": 10.0, "enabled_categories": [...] } }`.
**Errors:** `404` if inaccessible or nonexistent.

### `PATCH /api/v1/installations/:id/settings`
**Purpose:** update installation settings.
**Auth:** session. **Authorization:** requester must have an admin-capable role for `:id` (see [security.md](./security.md#authorization)).
**Request body:** partial `settings` object.
**Validation:** `default_mode` must be one of the six defined modes; `budget_daily_usd` must be a positive number.
**Response `200`:** updated settings object. **Errors:** `400` invalid field, `404` inaccessible, `403` insufficient role.

### `GET /api/v1/repos?installation_id=`
**Purpose:** list repos under an installation.
**Parameters:** `installation_id` (uuid, required).
**Response `200`:** `{ "data": [Repository] }`.

### `GET /api/v1/repos/:repoId`
**Purpose:** repo detail including parsed config.
**Response `200`:** `{ "repo": Repository, "config": {...} }`. **Errors:** `404`.

### `PATCH /api/v1/repos/:repoId`
**Purpose:** toggle `is_active`, override parsed `.prrisk.yml` config.
**Request body:** `{ "is_active": bool, "config_override": {...} }` (both optional, at least one required).
**Validation:** `config_override` validated against the `.prrisk.yml` schema (FR-111).
**Response `200`:** updated repo object. **Errors:** `400`, `404`.

### `GET /api/v1/reviews`
**Purpose:** list reviews, tenant-scoped.
**Parameters (query):** `installation_id` (required), `repo` (optional), `status` (optional, enum), `band` (optional, enum), `mode` (optional, enum), `from`/`to` (optional, ISO 8601 dates), `cursor`, `limit`.
**Response `200`:** `{ "data": [ReviewSummary], "next_cursor": "string|null" }`.
**Validation:** unknown `status`/`band`/`mode` values → `400`.

### `GET /api/v1/reviews/:reviewId`
**Purpose:** full review detail — scores, signals, stats, contribution breakdown.
**Response `200`:** `{ "review": ReviewDetail, "breakdown": [Contribution] }`. **Errors:** `404` (not found or not accessible).

### `GET /api/v1/reviews/:reviewId/findings`
**Purpose:** findings for a review.
**Parameters:** `category`, `severity`, `min_confidence` (optional filters).
**Response `200`:** `{ "data": [Finding] }`. **Errors:** `404`.

### `GET /api/v1/reviews/:reviewId/impacts`
**Purpose:** per-file blast-radius data for a review.
**Response `200`:** `{ "data": [FileImpact] }`. **Errors:** `404`.

### `GET /api/v1/reviews/:reviewId/llm-calls`
**Purpose:** cost/latency breakdown for a review.
**Response `200`:** `{ "data": [LlmCall], "total_cost_usd": number }`. **Errors:** `404`.

### `POST /api/v1/reviews/:reviewId/rerun`
**Purpose:** trigger a new analysis of the same PR (typically a different mode).
**Request body:** `{ "mode": "security" }`.
**Validation:** `mode` must be one of the six defined modes.
**Responses:**
| Status | Meaning |
|---|---|
| `202` | `{ "review_id": "uuid" }` — new review enqueued |
| `409` | `{"error":{"code":"REVIEW_IN_FLIGHT", ...}}` — a review for this PR is already `queued`/`running` |
| `404` | review or installation not accessible |
| `400` | invalid mode |

### `GET /api/v1/reviews/:reviewId/events` *(nice-to-have, V1, non-blocking)*
**Purpose:** Server-Sent Events stream of status transitions for a review, for live dashboard updates.
**Response:** `text/event-stream`, one event per status change. Absence of this endpoint does not affect any other capability (FR-095).

### `GET /api/v1/analytics/overview?installation_id=&from=&to=`
**Purpose:** aggregate metrics — review counts by band, average score, average latency, total cost, findings by category.
**Response `200`:** `{ "review_counts_by_band": {...}, "avg_score": number, "avg_latency_ms": number, "total_cost_usd": number, "findings_by_category": {...} }`.

### `GET /api/v1/analytics/cost?installation_id=&from=&to=`
**Purpose:** cost per day, per model, per repo.
**Response `200`:** `{ "data": [{ "day": "date", "model": "string", "repo": "string", "cost_usd": number }] }`.

### `GET /api/v1/analytics/hotspots?installation_id=`
**Purpose:** files with highest cumulative risk contribution.
**Response `200`:** `{ "data": [{ "file_path": "string", "repo": "string", "cumulative_risk_contribution": number, "appearance_count": int }] }`.

---

## 5. Shared Response Shapes (reference)

```jsonc
// ReviewSummary
{ "id": "uuid", "pr_number": 42, "repo": "owner/name", "head_sha": "abc123",
  "status": "completed", "risk_score": 62, "risk_band": "high",
  "review_mode": "standard", "created_at": "2026-01-01T00:00:00Z", "total_duration_ms": 48213 }

// ReviewDetail (extends ReviewSummary)
{ ...ReviewSummary, "category_scores": {"security": 0.71, "bug": 0.40, ...},
  "signals": {...}, "stats": {...}, "scoring_version": "v3", "summary_md": "..." }

// Contribution
{ "label": "sensitive_path_touched", "value": 0.15, "weight": 1.0 }

// Finding
{ "id": "uuid", "category": "security", "severity": "high", "confidence": 0.82,
  "title": "string", "description_md": "string", "file_path": "string",
  "start_line": 10, "end_line": 12, "anchored": true, "posted_as_comment": true }
```

## 6. Endpoints Not Included (and why)

To satisfy "do not create unnecessary endpoints": no CRUD on `findings`/`file_impacts` beyond read (findings are system-generated, never user-edited, per the non-goal that this is not an auto-fix or manually-curated rules tool). No `/users` management endpoints beyond `/me` (no RBAC/team management per PRD non-goals). No billing endpoints (out of scope). No generic webhook-relay or export endpoints (not requested by `plan.md`).
