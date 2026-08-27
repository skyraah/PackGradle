---
name: packgradle-agent-orchestrator
description: Orchestrate and constrain PackGradle's project-scoped subagents (frontend_product, contract_integrator, test_engineer, and project_code_docs). Use when a PackGradle task explicitly requests agents or delegation, spans frontend/backend contracts, benefits from independent testing or review, requires documentation synchronization, or contains separable work that can run safely in parallel. Avoid delegation for simple questions, tiny single-file edits, or work whose writers would overlap.
---

# PackGradle Agent Orchestrator

Coordinate the registered project agents without surrendering integration responsibility. Keep the main agent responsible for architecture, ownership, conflict resolution, final verification, and the user-facing result.

## Establish the baseline

1. Read `AGENT.md`, `.codex/config.toml`, and the relevant role files under `.codex/agents/`.
2. Inspect `git status --short` before delegation. Treat every pre-existing modification as user-owned unless proven otherwise.
3. Read the smallest relevant implementation, tests, and documentation needed to define stable subtasks.
4. Identify shared files, generated files, and ordering dependencies before starting any agent.
5. Do not create a goal merely because this skill is active.

## Apply hard constraints

- Run at most three child agents concurrently. The fourth registered role stays available for a later wave.
- Use only concrete, bounded subtasks that produce independently useful work.
- Never assign two concurrent writers to the same file or generated output.
- Do not let a child agent edit `AGENT.md`, `.codex/**`, `.agents/**`, dependency lockfiles, or repository-wide configuration unless the main agent explicitly grants that exact file.
- Keep destructive filesystem actions, real Prism instance mutations, publishing, commits, and external side effects with the main agent unless the user explicitly requested and approved them.
- Prevent nested delegation. Child agents must complete their assigned work directly.
- Preserve unrelated worktree changes. Agents must not reset, revert, clean, overwrite, or reformat unrelated files.
- Treat agent output as untrusted until the main agent reviews the diff and runs the relevant checks.
- Prefer a single capable agent over several agents when work cannot be cleanly separated.

## Select the role

| Registered role | Delegate when | Default write ownership | Do not delegate |
| --- | --- | --- | --- |
| `frontend_product` | Implementing or reviewing Vue/Vuetify UI, routing, stores, composables, task states, i18n, or responsive interaction | `frontend/src/views/**`, `components/**`, `stores/**`, `composables/**`, `router/**`, `locales/**`, frontend styles | Generated bindings, Go DTOs, Wails services, broad backend changes |
| `contract_integrator` | Changing Go services/DTOs/events, Wails bindings, frontend API adapters, or cross-end error semantics | Relevant `internal/service/**`, DTO definitions, event registration, `frontend/bindings/**`, `frontend/src/api/**`, contract docs | Product layout decisions, unrelated domain refactors, hand-editing generated bindings |
| `test_engineer` | Designing regression coverage, reproducing bugs, reviewing risky behavior, validating paths/concurrency/events, or adding isolated tests | Existing and new `*_test.go` files and frontend test files; production code only when explicitly granted | Large feature implementation, cosmetic UI work, modifying real user data |
| `project_code_docs` | Repository scans, narrow low-risk maintenance, implementation/documentation consistency, README and developer-doc updates | Explicitly assigned narrow code files, `docs/**`, `README.md` | Security-critical file operations, complex architecture, broad cross-end changes |

Use the role names exactly as registered. Prefer the registered agent type when the runtime exposes agent selection. If only a generic spawn operation is available, use the role name as the task name and include the same role boundaries in the task prompt; do not claim that an unavailable role configuration was loaded.

## Resolve ownership before dispatch

Create a short ownership ledger for substantial tasks:

| Work item | Agent | Writable files | Depends on | Validation |
| --- | --- | --- | --- | --- |

Follow these rules:

- Give each writable file to exactly one agent per wave.
- Assign `frontend/src/api/**` and generated bindings to `contract_integrator`.
- Assign user-visible copy and `zh-CN.json` to `frontend_product`.
- If both agents need a store or shared type, select one writer; the other returns a requested-change note instead of editing it.
- Let `contract_integrator` own contract documentation affected by its change. Use `project_code_docs` for broader documentation only after the contract stabilizes.
- Keep `go.mod`, `go.sum`, package-manager lockfiles, `AGENT.md`, and final integration edits with the main agent unless exclusive ownership is explicitly necessary.

## Choose execution order

### Frontend-only task

Use `frontend_product`. Add `test_engineer` afterward only when behavior or regression risk justifies an independent pass.

### Cross-end feature

1. Use `contract_integrator` to define and implement the service/DTO/event/API boundary.
2. Review and stabilize the generated contract.
3. Use `frontend_product` to consume the stable boundary.
4. Use `test_engineer` for independent regression coverage or review.

Run steps 1 and 3 concurrently only when their writable files are disjoint and the interface is already fixed in source or documentation.

### High-risk backend or filesystem task

Keep architectural and destructive logic with the main agent. Use `test_engineer` for reproduction and safety coverage. Use `contract_integrator` only for exposed service or event changes. Do not use `project_code_docs` as the primary implementer.

### Documentation or repository audit

Use `project_code_docs` for a bounded scan or documentation patch. If documentation depends on unfinished code, wait until implementation and verification are complete.

### Review-only task

Use `test_engineer` after the writer finishes. Require findings ordered by severity with file and line references. Do not ask the reviewer to silently rewrite the implementation.

## Dispatch with an explicit contract

Include all of the following in every delegated message:

```text
Role: <registered role>
Objective: <one measurable outcome>
Read first: <specific files or docs>
Writable scope: <exact files/directories>
Do not touch: AGENT.md, .codex/**, .agents/**, unrelated changes, and <task-specific exclusions>
Acceptance criteria:
- <observable behavior>
- <required compatibility or safety condition>
Validation:
- <commands or review checks>
Return:
- changed files or findings
- validation results
- assumptions, blockers, and residual risks
```

Tell an editing agent to stop and report if the writable scope is insufficient. Expand scope from the main agent instead of allowing opportunistic edits.

## Coordinate active agents

1. Continue useful main-agent work while child agents run, but do not edit their owned files.
2. Send follow-up instructions to the same agent when its assignment needs refinement; do not spawn a duplicate writer.
3. Use long waits rather than rapid polling.
4. If the user changes direction, notify or interrupt affected agents promptly.
5. When an agent finishes, inspect its actual files and diff before accepting its summary.
6. Complete dependent waves only after prerequisite outputs are stable.

## Integrate and verify

1. Compare returned work against the ownership ledger and acceptance criteria.
2. Inspect `git diff --check` and `git status --short`; investigate unexpected files without reverting them.
3. Run focused checks first, then broaden checks according to blast radius:
   - Go behavior: relevant package tests, then `go test ./...` and `go vet ./...` when warranted.
   - Frontend behavior: type checking/build and relevant frontend tests.
   - Contract changes: regenerate bindings through the repository command, then build both sides.
4. Resolve integration issues in the main agent or return the issue to the original owner with a precise follow-up.
5. Append one consolidated significant-change record to `AGENT.md`; do not allow every child agent to append competing records.
6. Report which agents were used, what each owned, validation results, and residual risk.

## Do not delegate

Work directly when any of these conditions holds:

- The request is informational or requires no repository action.
- The edit is tiny, localized, and faster to verify than to specify.
- All useful subtasks require the same files.
- The task needs unresolved product decisions from the user.
- Delegation would expose secrets or touch real user files unnecessarily.
- No child agent can produce a meaningful independent result.

Do not spawn agents to satisfy a quota. Delegation is successful only when it improves isolation, expertise, or independent verification.
