# Task branch strategy and prompt trust

> Applies to the sandboxed task runner (`aisotropy.App.RunTask` / `App.RunTasks`,
> behind the `keybroker` build tag). Scope: the supply-chain posture of
> `TaskRequest.BranchStrategy`.

## The risk: auto-merge of untrusted agent output

`TaskRequest.BranchStrategy` decides what happens to the commits a sandboxed
coding task produced:

- **`MergeToHead`** (the zero value / default): the task branch is merged into the
  operator's repo `HEAD` automatically, with no human gate.
- **`ReturnBranch`** (a.k.a. `NamedBranch`): the commits are left on the task
  branch, unmerged, for the caller to review, merge, or open a PR from.

The default is convenient for a **trusted** task source, but it is a
**supply-chain risk when the task `Prompt` is untrusted** — attacker-influenced
text coming from an end user, an issue tracker, a webhook, a scraped page, etc.

The key-broker invariant protects the *provider credential* (it never enters the
sandbox), and the sandbox hardening constrains what the task can *do to the host*
(no network, dropped capabilities, read-only rootfs, wall-clock deadline). None of
that constrains what the model chooses to **commit**. An adversarial prompt can
steer the agent into committing hostile or subtly-backdoored code; with
`MergeToHead` that code then lands in the operator's `HEAD` automatically. The
attacker's payload has crossed from "sandboxed output" into "your source tree"
without any human inspection.

## Recommendation

| Task source | Recommended `BranchStrategy` | Why |
|---|---|---|
| **Untrusted** prompt (end-user / external / scraped) | `ReturnBranch` | The commits stay on an unmerged branch; a human (or a separate review gate) inspects them before they can reach `HEAD`. |
| **Trusted / reviewed** prompt (operator-authored, internal automation you control) | `MergeToHead` (default) | Convenience is appropriate when the input is already trusted. |

Treat `ReturnBranch` as the **review-before-merge** default posture for anything
you did not write yourself, and reserve the auto-merge default for prompts whose
source you trust.

The default behaviour is **deliberately left unchanged** (the zero value is still
`MergeToHead`) to avoid a silent breaking change for existing trusted callers —
see the security gate on backward-compatible API contracts. This document plus the
`SECURITY:` doc comments on `TaskRequest.BranchStrategy` (public façade) and
`taskagent.Request.BranchStrategy` (internal) make the risk explicit at the call
site so the choice is a conscious one.

## Proposal (non-breaking): make the choice explicit for untrusted callers

Because the *zero value* is the risky one, an untrusted caller who simply forgets
to set `BranchStrategy` silently gets auto-merge. A future, **additive** hardening
that does not break the existing API could close that gap, for example:

- A constructor such as `NewUntrustedTaskRequest(...)` that returns a
  `TaskRequest` pre-set to `ReturnBranch`, so the safe posture is the default for
  callers who opt into it.
- A boolean guard (e.g. `TaskRequest.RequireExplicitBranchStrategy` /
  `UntrustedPrompt`) that makes `RunTask` refuse to auto-merge unless
  `BranchStrategy` was set explicitly — turning the dangerous default into a
  fail-closed error for callers who declare the prompt untrusted.

Both are strictly additive (new symbols / new opt-in fields); neither changes the
meaning of the existing zero value, so no current caller breaks. They are recorded
here as options, **not implemented** in this change.
