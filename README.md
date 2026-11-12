# worktree

Fresh Git worktrees with global inventory, optional count/disk budgets, task protection, and explicit safe pruning. There is no reusable checkout pool.

`worktree` is a standalone Go executable for macOS and Linux, on ARM64 and x86_64. Installed machines do not need Python or Go. Git is required; cleanup also uses the OS's Bash, `lsof`, and standard Unix utilities. Optional `sqlite3` adds read-only Codex session reporting.

## Install

Download the appropriate executable and adjacent SHA256 file from the versioned release, or use the installer:

```sh
curl -fLsS https://github.com/lanej/worktree-manager/releases/download/v0.1.0/install-worktree -o /tmp/install-worktree
sh /tmp/install-worktree 0.1.0
worktree --version
```

The installer places the executable at `~/.local/bin/worktree`, verifies its checksum and version, and records an ownership receipt. Repeated installations reuse a verified binary. Existing unmanaged executables, symlinks, modified binaries, and destinations changed during downloading are retained with an error. For an offline package, set `WORKTREE_INSTALL_BINARY=/absolute/path/to/worktree-PLATFORM-ARCH`; keep its adjacent `.sha256` file.

## A task from creation to cleanup

```sh
# From an existing repository; the default .worktrees/ destination must be ignored.
worktree create --branch task/example --json
# Use the returned path and task ID. Each request creates a fresh named branch/checkout.
worktree status
worktree finish TASK_ID
worktree prune --all                 # Preview only.
worktree prune --all --apply         # Recheck and explicitly remove eligible candidates.
```

Creation defaults to local `HEAD` and never fetches or resets an existing branch. Use `--base REF`, `--path PATH`, and `--repo PATH` to choose the source or destination. An explicit path is required for bare repositories. The default path is `<primary>/.worktrees/task-<id>`; an existing directory is never reused.

New tasks have a native Git lock and a durable task record. Finishing removes only the task's own lock and changes its lifecycle state; it does not delete the checkout. A manual Git unlock does not mark the task finished. Locks changed by another owner are preserved. Native Git remains authoritative: moving a checkout preserves its task identity, while a later checkout reusing its old path is a separate registration.

If a hook fails after Git has created a checkout, creation can return nonzero with an active task and an existing path. If the result cannot be confirmed, the task remains uncertain and protects its associated repository until explicitly finished. Inspect the returned result and Git state before retrying. Do not assume that an error rolled back Git.

## Discover and track existing worktrees

```sh
worktree backfill ~/src ~/.files
worktree status --json
```

Backfill scans only the roots you specify. It stops at each discovered repository, skips dependency/build directories, deduplicates common Git directories, and discovers every linked worktree through Git even when it lives outside those roots. It registers inventory without claiming task ownership, editing checkout files, or deleting unavailable records. There is no automatic home-directory scan, daemon, or scheduled pruning.

To observe future Git events, generate a separate configuration fragment:

```sh
worktree hooks --file ~/.config/worktree-manager/hooks.gitconfig
```

Include that fragment from your own Git configuration. `worktree hooks` without `--file` prints the configuration. The command does not edit your main Git configuration, change `core.hooksPath`, or replace existing repository hook files.

Configured hooks require Git 2.54 or later. Dedicated `pre-worktree` and `post-worktree` events require the [patched Git runtime](https://github.com/lanej/dotfiles/releases/tag/git-worktree-hooks-2.56.0.1); those hooks observe add/move/remove/prune, including `--no-checkout`, and deliver admission vetoes. Stock Git offers checkout observation. For older Git using default repository hooks, `worktree policy install` preserves existing originals through compatibility dispatchers; configured hooks managers can invoke `worktree policy observe EVENT ...` themselves. Existing `worktree policy hook` dispatchers chain saved originals; `observe` does not.

## Optional limits

```sh
worktree limits --max-worktrees 4 --max-bytes 8GiB
worktree limits --global --max-bytes 40GiB
worktree limits --max-worktrees off
```

Budgets are disabled by default and cover linked checkouts, excluding primary checkouts and shared Git objects. Local budgets retain the `worktree.limit` and `worktree.maxBytes` Git settings. Global budgets cover every registered repository. Allocated disk accounting includes ignored build outputs, skips symlinks, and deduplicates hardlinks; shared copy-on-write extents can conservatively overstate physical usage.

`create` checks capacity with either stock or patched Git. Patched Git also checks external worktree additions; stock-Git callers can run `worktree policy preflight` first. Checks do not reserve space: concurrent creation, a new checkout's size, or subsequent build growth can exceed a budget. Subsequent admission sees actual usage and refuses more allocation. Limits never delete existing files. Unevaluable configured budgets block admission; event notifications warn without rejecting ordinary checkout.

## Full cleanup behavior

`worktree prune` retains the existing dotfiles cleanup capabilities. It previews by default. `--apply` or `-y` explicitly applies, and only candidates from that invocation's preview can be removed. Every candidate is checked again immediately before removal. Branches are preserved.

The default retains primary checkouts, active/uncertain tasks, native locks, active processes, dirty/unmerged work, unfinished Git operations, unresolved index conflicts, hidden index flags, valuable ignored artifacts, and recent HEAD commits. The age threshold defaults to 24 hours of **HEAD commit age**, not checkout creation time. Default base selection uses each repository's local `origin/HEAD`, then local `main` or `master`; no implicit fetch occurs. Treehouse pool paths remain reserved while the tools coexist.

Use `--worktree PATH` for one checkout, a positional root to discover repositories beneath it, or `--all` for the known registry. `--integrated` accepts identical file trees only when a named branch retains the original HEAD. `--min-size`, `--refresh`, and `--conflict-files` control filtering and reporting. Sizes are cached for 15 minutes for display, while minimum-size admission uses fresh measurements. Reports include retained reasons, conflict details, allocated sizes, cache freshness, and optional Codex session matches. Missing activity inspection retains candidates; unavailable Codex metadata is labeled rather than treated as evidence of inactivity.

`--accept-dirty`, `--accept-ignored`, `--accept-unmerged`, and `--accept-recent` relax only their respective checks. `--force` selects those four exceptions. They never override task protection, locks, process activity, unresolved conflicts, unfinished operations, missing safety evidence, or branch retention for detached unmerged history.

`--wip-push` instead preserves dirty or unmerged work on a new recovery branch, pushes the exact snapshot, verifies the remote ref, and rechecks the checkout before removal. It excludes force/dirty/ignored/unmerged acceptance flags and allows `--accept-recent`. Recovery destinations are disabled unless explicitly approved in `~/.config/worktree-manager/config.json`:

```json
{"recovery_allowed_github_owners": ["easypost-sandbox"]}
```

`WORKTREE_CONFIG` can select another configuration file. Effective push URLs and rewrites are checked; publication must remain on the approved GitHub owner. Failed or uncertain publication/verification retains the checkout and any already-created recovery branch.

Run `worktree prune --help` for the complete inherited cleanup contract. The first release embeds the existing Bash cleanup mechanism; Go provides task protection, orchestration, JSON reporting and confirmed removal receipts. No Python runs in the installed cleanup path.

## JSON, storage and integration

All public data commands support `--json` with a `schema_version: 1`, `command`, and `result` envelope. Exact numeric values are decimal strings. Filesystem-byte surrogate escapes remain intact. Failures before a result use an `error` envelope with `code`, `outcome`, and `message`; stderr carries diagnostics. A nonzero exit with a result can describe a confirmed checkout plus a hook failure or an incomplete report.

Prune rows retain their preview `action` and reason; `outcome` distinguishes `not_attempted`, `retained`, `removed`, and `unknown`. `removed` requires a successful native removal receipt and reconciliation confirming that registration and directory are gone. Apply output includes mutation logs and diagnostics. Task reports distinguish recorded and observed paths and do not infer completion from absent processes or missing directories.

The original canonical common-Git-directory identities and JSON files under `$XDG_STATE_HOME/git-worktree-policy` remain compatible, including arbitrary-size limits. New tasks live under `$XDG_STATE_HOME/worktree-manager/tasks`; a token in each owned Git registration prevents association with a replacement registration. The default state root is `~/.local/state`. `worktree policy` retains the old policy command interface and JSON output.

Octo Manager can consume task IDs and JSON through the CLI in a future adapter. This release does not change its job model or private repair repositories, migrate Treehouse leases, or change agent defaults.

## Development

Use the Go version in `go.mod` and Python 3.11+ for development checks. Runtime binaries are built with `CGO_ENABLED=0`; the committed Go race checks additionally need a C compiler.

```sh
bash scripts/check-go.sh
scripts/test.sh
scripts/build-release.sh /tmp/worktree-packages
python3 scripts/test-install.py --binary /tmp/worktree-packages/worktree-PLATFORM-ARCH
```

Set `WORKTREE_HOOKS_GIT=/path/to/patched/bin/git` to include actual lifecycle-hook acceptance. The native workflow tests macOS/Linux on ARM64/x86_64, uses the existing pinned patched-Git release, and does not recompile Git. Release only artifacts from the identified source commit after applicable checks; retain published versions unchanged.

Tracking and cleanup were initially extracted from [lanej/dotfiles](https://github.com/lanej/dotfiles/tree/17cd6d522a139449f59c0f7a0e4c012054022124), preserving the existing policy and cleanup detectors.
