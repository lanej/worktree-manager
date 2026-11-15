# Agent worktree allocation

New agent checkouts live at `<primary-repository>/.worktrees/<agent>-<short-id>/`, such as `.worktrees/codex-a3f92c1e/`. Allocation must work before a task description or native agent session ID exists. The directory remains stable when the task or branch name changes.

## Verified starting point

Upstream `lanej/worktree-manager` main was verified at `9baa5e9479199220311539ea4740898cdc592fc6`. This assigned checkout is at initial commit `c13a23f`; implementation must start from the current upstream implementation while preserving this plan.

Upstream `tasks.go` already resolves the primary checkout, requires ignored default destinations, generates a full random 128-bit task ID, creates a fresh branch and protected checkout, and reconciles partial Git outcomes. Its default directory is `.worktrees/task-<full-id>` and `--branch` is mandatory. Extend that implementation rather than introducing another allocator.

Dotfiles already has a checksum-verified prebuilt installer in `scripts/install-worktree-manager`, pinned to v0.1.0, and an optional `worktrees` set. Its `ai` set still selects Treehouse. Codex and Claude wrappers call `bin/agent-treehouse`; Paseo Tasks also documents Treehouse allocation. Existing cleanup distinguishes active task protection from finished work and preserves legacy Treehouse pools.

## Allocation contract

- Add `--agent SLUG` and optional `--owner-id ID` to `worktree create`. Agent integrations pass a stable client label and their full allocation/session token; direct human use defaults to `manual`. Validate the agent slug before filesystem or Git mutation; never use an untrusted identity as a path fragment.
- Keep the full existing random task ID as the durable identity. Use its first eight hexadecimal characters in the directory name. Retry generated identities on generated path or branch collisions with a bounded attempt count. Never reuse or remove an existing destination.
- Make `--branch` optional. When omitted, generate `worktree/<agent>-<short-id>`. Explicit branch names retain their existing validation and refusal to overwrite. Keep the existing local `HEAD` base default and explicit `--base` behavior.
- Resolve the destination from the primary checkout even when invoked inside a linked checkout. Agent callers use the canonical default destination rather than supplying `--path`. Preserve the existing explicit-path facility for deliberate CLI use, including bare repositories, and the rejection of paths inside Git administration directories.
- Verify that the resolved default destination remains within the primary checkout's `.worktrees/` directory, including symlink resolution, and is ignored. Reject unsafe or unignored destinations before creating a branch or durable task record. Do not silently fall back to a temporary directory.
- Store additive `agent` and optional `owner_id` fields in task records and JSON results. Retain the existing record IDs, registration markers, locks, lifecycle states and finish behavior. Older records without ownership fields remain readable. Show the owner alongside the task ID, branch and path in human-readable output.
- Native session identity may become available only after launch. The launcher keeps its existing mapping from allocation token to native sessions; do not invent a session ID or require one before allocation. A later descriptive branch rename needs no directory rename or loss of task identity.

## Delivery sequence

1. Implement the allocation contract in `tasks.go`, CLI help in `main.go`, human output in `show.go`, and the README. Extend the real-Git lifecycle checks and focused path/identity tests. This is the first independently verifiable delivery.
2. Review the source change independently, run the repository's Go quality and lifecycle checks, and build the existing macOS/Linux ARM64/x86_64 release artifacts. Publish a new immutable version with checksums; do not replace v0.1.0 assets. Pin the new version in dotfiles only after the artifacts exist.
3. Replace fresh-allocation calls in the Codex/Claude launcher with `worktree create --agent CLIENT --owner-id TOKEN --json`. Consume the existing JSON envelope and task ID, including nonzero results that confirm a surviving checkout. Preserve argument/settings forwarding, assigned-checkout reuse, native session tracking and conservative activity checks. Rename the integration for its new responsibility and update wrappers, docs and CI references.
4. Replace lease return for new-manager records with explicit `worktree finish` after verified session completion. Keep checkout deletion in explicit safe pruning. Retain a legacy handler for existing Treehouse ownership records until outstanding leases are resolved; never pass a legacy lease to the new manager's finish/prune path as owned work.
5. Switch dotfiles' agent defaults and canonical worktree guidance to the new manager. Stop selecting Treehouse for fresh agent allocations. Update Paseo Tasks' allocator through the same CLI contract after inspecting its current source. Preserve assigned checkouts and existing pools; retire legacy installation/return support only when its users and leases are accounted for.

## Enforcement boundary

The allocator and migrated launchers enforce this convention for new allocations they own. Missing or failing selected-manager allocation must fail visibly instead of falling back to native temporary placement. No new Git fork or hook is needed for this scope. Arbitrary direct Git commands and host-managed desktop allocation are separate entrypoints; do not claim their names are enforced by this CLI.

The assigned checkout's `.codex/config.toml` already points desktop allocation at `<primary>/.worktrees`, but this checkout itself was host-created elsewhere. Verify the host's effective setting during integration; do not move active host-assigned checkouts or promise its generated leaf names match the manager's naming rule.

## Acceptance evidence

- Real Git creation from both a primary checkout and a linked checkout produces a fresh ignored `.worktrees/codex-<8-hex>` directory under the same primary repository, without requiring a task name or explicit branch.
- JSON and human output identify the full allocation, agent, optional owner, actual branch and checkout path. Legacy records load successfully; a branch rename retains registration-based identity.
- Invalid slugs, symlink escapes, unignored destinations, generated collisions, explicit branch collisions and capacity refusals do not overwrite existing work. Hook failures retain the existing reconciled outcome and protection behavior.
- The new binary installs and reinstalls from verified release artifacts. Disposable-home dotfiles checks prove provider selection, missing-binary failure and preservation of unmanaged installations/configuration.
- Codex and Claude workflow checks exercise fresh creation, resumed/assigned checkout reuse, session completion, partial allocation failures and legacy-record coexistence. Finished work remains on disk until explicit pruning; active or uncertain work remains protected.
- Verify applicable checks and published assets separately from installed version and live launcher acceptance. Report any host or Paseo integration still outstanding.
