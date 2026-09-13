# Product Requirements Document — GitHub PR Risk Analyzer

**Source of truth:** [`../pr-risk-analyzer-plan.md`](../pr-risk-analyzer-plan.md)
**Related documents:** [requirements.md](./requirements.md) · [user-flows.md](./user-flows.md) · [HLD.md](./HLD.md) · [architecture-decisions.md](./architecture-decisions.md)

---

## 1. Product Overview

The GitHub PR Risk Analyzer is a GitHub App that automatically analyzes every pull request opened or updated on an installed repository and returns a deterministic, explainable **risk score (0–100)** and a set of categorized **findings** (bugs, security issues, performance concerns, architectural issues, test gaps). Results are delivered as a GitHub Check Run, a sticky summary comment, and high-confidence inline review comments, and are also browsable through a web dashboard.

The system is positioned as a **risk intelligence system**, not an "AI code review" tool. The LLM is one signal provider among several (deterministic heuristics: churn, sensitive-path detection, dependency-change analysis, test-gap detection). The score itself is always produced by a pure, versioned, deterministic function — never by the model directly. (`plan.md`, header positioning statement and §7)

## 2. Problem Statement

Review effort does not scale with the size or sensitivity of a change. A trivial typo fix and a 900-line change to an authentication module currently compete for the same reviewer attention with no automated signal distinguishing them. Generic "AI review" tools that ask an LLM to eyeball a diff and produce prose are not reproducible, not explainable, and not defensible — teams disable them once they see a confidently wrong comment. There is no system that combines deterministic, auditable risk signals with LLM-found issues into a single score that a reviewer can trust and act on, and that an engineer can defend the way they'd defend a linter rule.

## 3. Goals

Derived from `plan.md` §1 (non-negotiable architectural rules), §7, §11, and §19:

1. Give every PR a fast (`<1s` webhook ack, ~60s standard-mode turnaround), automated first-pass risk signal.
2. Make every score **explainable** — a full contribution breakdown must answer "why 78?" without reading code (`plan.md` §7).
3. Make every score **reproducible** — identical inputs (diff + signals + LLM fixture) always yield identical output (`plan.md` §12 "Rules").
4. Keep false positives low enough that the tool is not disabled (`plan.md` §17, "Posting every finding as an inline comment... developers disable the app within a week").
5. Never fail silently — every documented failure mode still produces a usable result (`plan.md` §11, "Degradation ladder").
6. Control and fully attribute LLM cost per review, per installation, and globally (`plan.md` §6 Stage 5, §10 "Abuse and cost").
7. Prove the system works via a committed evaluation harness, not anecdote (`plan.md` §12 "Evaluation harness", §19 item 5).

## 4. Non-Goals

Directly from `plan.md` §14 "Deliberately out of scope" and architectural rules:

- The LLM does **not** assign the risk score under any circumstance (`plan.md` §1 rule 4, §7).
- No auto-fix commits or auto-generated patches.
- No multi-VCS support (GitLab, Bitbucket) — GitHub only.
- No IDE extensions, no Slack/Discord notifications.
- No billing/subscriptions, no team/RBAC management.
- No fine-tuned or self-hosted models, no custom rule DSL.
- The Check Run conclusion is never `failure` — the bot never blocks merges (`plan.md` §7 "Conclusion policy", §17).
- Style/nit findings are dropped by default, not surfaced as noise (`plan.md` §6 Stage 4).

## 5. Target Users

| User | Relationship to the system |
|---|---|
| **PR Author** | Receives the check run and inline comments on their own PR |
| **Reviewer** | Uses the summary/inline comments and dashboard breakdown to focus review attention |
| **Engineering Manager / Tech Lead** | Uses the dashboard's analytics and hotspot views to understand risk trends and cost |
| **Installer / Repo Admin** | Installs the GitHub App, configures `.prrisk.yml`, sets budget/mode defaults |

## 6. User Personas

*(Lightweight personas — the plan does not describe a multi-tenant SaaS sales motion, so personas are kept to the roles actually interacting with the system, per `plan.md` §2 and §9.)*

- **Priya, Backend Engineer (PR Author/Reviewer).** Opens 3–5 PRs a week. Wants a fast signal on her own risky changes before a human looks, and wants review comments on others' PRs to be trustworthy enough to act on without re-verification.
- **Marcus, Tech Lead (Admin/Manager).** Installed the app on his team's repos. Wants to configure sensitive paths and review modes per repo, keep LLM spend predictable, and periodically check which files are chronic risk hotspots.

## 7. Core Use Cases

Full step-by-step flows are in [user-flows.md](./user-flows.md). Summary list, each traceable to a `plan.md` section:

1. A PR is opened → analyzed → check run + summary + inline comments posted. (`plan.md` §5, §6, §7)
2. A PR receives a new push → the prior review is superseded, not duplicated. (`plan.md` §5 "Subscribed events", §8 "Supersession")
3. A PR is closed → any in-flight review is cancelled. (`plan.md` §5)
4. A reviewer triggers a manual, deeper analysis via `/prrisk review --mode=deep`. (`plan.md` Phase 8)
5. The LLM provider is unavailable or over budget → a degraded, signals-only review still completes. (`plan.md` §11 "Degradation ladder")
6. A webhook is redelivered by GitHub → the system produces zero duplicate work. (`plan.md` §5 "Handler sequence", §11)
7. An admin logs into the dashboard via GitHub OAuth and reviews score history, cost, and hotspots. (`plan.md` Phase 9, Phase 10)
8. An admin re-runs a specific review in a different mode from the dashboard. (`plan.md` §4 `POST /reviews/:reviewId/rerun`)

## 8. Functional Requirements (Summary)

Full, testable, ID'd requirements are in [requirements.md](./requirements.md). Capability summary, grouped by pipeline stage (`plan.md` §1 "Runtime topology", §2):

- **Ingestion:** HMAC-verified webhook receipt, delivery-ID dedup, fast ack, event-type routing, skip conditions with recorded reasons.
- **Queueing:** Idempotent, retryable, prioritized async task execution; supersession on new pushes; stale-task reaping.
- **Context building:** Diff parsing, line→position mapping, file classification, neighbor resolution, secret redaction, token budgeting.
- **Deterministic signals:** Size/churn metrics, sensitive-path detection, dependency-change classification, test-gap detection, secret detection — computed without any LLM call.
- **LLM analysis:** Schema-constrained structured output, map-reduce over chunks, evidence-anchored hallucination filtering, deterministic confidence calibration, model routing by computed complexity, escalation on high-severity findings.
- **Scoring:** Pure, versioned, deterministic 0–100 score with category subscores, blast-radius multiplier, floors/ceilings, and a full contribution breakdown.
- **Publishing:** Check run lifecycle, sticky (upserted) summary comment, capped high-confidence inline comments, dry-run mode.
- **Dashboard:** OAuth login, tenant-scoped review history, score breakdown UI, cost/latency views, analytics and hotspots.
- **Cost/abuse control:** Per-review/installation/global budget caps with graceful degradation, per-installation and per-IP rate limiting, comment-per-PR cap.

## 9. Non-Functional Requirements (Summary)

Full detail in [requirements.md](./requirements.md#non-functional-requirements). Summary, from `plan.md` §1, §10, §11:

- Webhook acknowledgment under 1 second, always.
- Standard-mode review turnaround around 60 seconds.
- Deterministic reproducibility of any historical score from stored inputs.
- No component ever leaves a check run in a non-terminal state.
- Tenant isolation enforced at the data-access layer.
- No PAT usage anywhere, including local development.
- No secrets, diffs, or tokens ever written to logs.
- Every LLM call attributed to a review, model, token count, and cost.

## 10. MVP Scope

Per `plan.md` §14 and §18: **Phases 0–7.** GitHub App registration and webhook ingestion; async queue/worker split with idempotency and supersession; diff parsing, context building, and the deterministic signal layer; LLM findings with schema validation and evidence anchoring; the pure risk-scoring engine; and GitHub publishing (check run, sticky comment, inline comments). Standard review mode only. One LLM provider. No dashboard, no analytics, no multi-mode routing.

> `plan.md` §14: "This is a genuinely useful tool and a complete demo." §18: "M1–M7 alone (about 10 weeks) is a complete, defensible, portfolio-grade project."

## 11. Future Scope

- **V1 (Phases 8–10):** Six review modes, complexity-based model routing and escalation, budget-cap degradation, slash commands, per-repo `.prrisk.yml`, the web dashboard (auth, review history, score breakdown), and analytics/cost views.
- **V2 (Phases 11–12):** Circuit breakers, GitHub rate-limit management, dead-letter queue with replay, load testing, production deployment, and the published evaluation harness.
- Explicitly deferred indefinitely (`plan.md` §14): IDE extensions, chat notifications, auto-fix PRs, multi-VCS, billing, RBAC, fine-tuned/self-hosted models, custom rule DSL.

## 12. Success Criteria

From `plan.md` §12 "Evaluation harness" and §18 "Checkpoints":

- Band accuracy within ±1 band on a labeled set of 25–40 real PRs.
- Measured true-positive rate on known-issue PRs and false-positive rate on trivial PRs.
- Score variance across 3 repeated runs of the same PR is effectively zero.
- After Phase 4: the tool produces useful output with the LLM entirely disabled (deterministic signals alone are meaningful).
- After Phase 7: running 10 real PRs from a known repo produces findings the author would actually act on.
- Cost, latency, and eval results are tracked per prompt/scoring version in a committed CSV.

## 13. Constraints

- Single engineer, part-time (~12–15 hrs/week) build, per `plan.md` §18 estimate basis.
- GitHub is the only supported VCS/webhook source.
- GitHub App authentication only — no OAuth App, no PAT, in any environment (`plan.md` §5, §10).
- PostgreSQL is the system of record; Redis holds only transient/queue state (`plan.md` §1, §3).
- Scoring package must depend on nothing but the Go standard library (`plan.md` §7, §13 Phase 6).
- No test may hit a real LLM provider or real GitHub in CI (`plan.md` §12 "Rules").

## 14. Assumptions

`plan.md` is treated as authoritative; where it leaves a decision open, the smallest reasonable assumption is made and flagged here (and re-referenced from the relevant document):

| # | Ambiguity in `plan.md` | Assumption made |
|---|---|---|
| A1 | LLM provider choice is left generic ("Start with one provider") | Provider is selected at implementation time behind the `llm.Provider` interface already specified; no specific vendor is architecturally required. Documented as an open question in the original PRD draft and unresolved here. |
| A2 | Saturation constants `K_c` in the scoring formula are described as "tuned per-category" with no starting values | Placeholder constants are set during Phase 6 calibration against real PRs, not fixed at design time; scoring remains versioned so this never blocks other work. |
| A3 | Dashboard session mechanism is "`gorilla/sessions` or a signed-cookie implementation" (either/or) | Signed, `HttpOnly`, `Secure`, short-lived cookie sessions are assumed for MVP simplicity — no separate server-side session store required. |
| A4 | Exact rate-limit numbers (reviews/hour per installation, requests/IP) are not specified | Treated as configurable values with conservative defaults set at implementation time, not hardcoded requirements. |
| A5 | Retention window is described as "N days" without a number | 30 days is assumed as the default retention for raw LLM responses and full diffs; findings and scores persist indefinitely. Configurable per installation later. |
| A6 | Inline comment cap is given as a range ("10–15") in §10 but fixed at "cap at 10" in §7's formula | 10 is treated as the authoritative default (the more specific, formula-adjacent number), configurable per repo. |
| A7 | Static analyzers (`semgrep`, `gitleaks`) are marked "optional" | Treated as optional/Phase-4 enhancements, not required for MVP completion. |
| A8 | Whether the Check Run conclusion policy (`neutral`/`success`, never `failure`) can ever become configurable is raised as an open question in §19-adjacent notes | For the scope of this documentation package, treated as a permanent MVP+V1 policy; opt-in blocking is out of scope entirely (see Non-Goals). |
| A9 | Exact budget cap dollar amounts are not specified | Treated as operator-configured environment values, not fixed requirements. |

---

**Document lineage:** `pr-risk-analyzer-plan.md` → PRD.md → [requirements.md](./requirements.md) → [user-flows.md](./user-flows.md) → [HLD.md](./HLD.md) → [database-design.md](./database-design.md) / [API.md](./API.md) → [LLD.md](./LLD.md) → [security.md](./security.md) / [testing-strategy.md](./testing-strategy.md) → [development-plan.md](./development-plan.md) · [architecture-decisions.md](./architecture-decisions.md)
