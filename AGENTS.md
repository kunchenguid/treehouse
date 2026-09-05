# Treehouse - Agent Guide

## What is this?

Treehouse is a Go CLI tool that manages a pool of git worktrees (or, with the opt-in jj backend, Jujutsu workspaces) for parallel AI coding agent workflows. It maintains reusable, pre-warmed worktrees so agents get isolated environments instantly.

## Project Structure

- `main.go` - entry point, calls `cmd.Execute()`
- `cmd/` - CLI commands (cobra): `get` (incl. `get --lease`), `enter`, `return`, `status`, `prune`, `destroy`
- `internal/config/` - config file loading (`treehouse.toml`)
- `internal/hooks/` - user-configured lifecycle hook command execution
- `internal/pool/` - pool manager (acquire, release, list, destroy, prune) + state file
- `internal/vcs/` - VCS backend seam: the `vcs.Backend` interface, backend selection (`backendFor`), and package-level wrappers the rest of the code calls
- `internal/vcs/gitvcs/` - git backend (shells out to `git` binary)
- `internal/vcs/jjvcs/` - Jujutsu backend (shells out to `jj` binary; pooled worktrees are jj workspaces)
- `internal/process/` - in-use detection and lingering process termination for worktrees
- `internal/shell/` - subshell spawning
- `internal/ui/` - Y/n confirmation prompts

## Building

```sh
go build -o treehouse .
# or
make build
```

## Testing

```sh
go test ./...
# or
make test
```

## Key Design Decisions

- No daemon - all operations are inline CLI commands
- Detached HEAD worktrees reset to whichever of local or origin default branch is further ahead (prefers origin on divergence)
- Acquire reuses a slot only when it is idle, unleased, clean, and HEAD is merged into that exact reset target (resolved once to an immutable commit so the guard and `ResetWorktreeToRef` cannot disagree) or into the base the slot itself records (`headMergedIntoRecordedBase`, which re-reads HEAD and fails closed on a mismatch or any error, then still resets through the requested base's resolved commit); an inferred acquisition and a state entry predating `base_branch` both record none, so the repository default stands in as its implicit base HERE ONLY - acquire may reset such a slot because its HEAD stays reachable from a local branch, while prune and destroy deliberately do NOT give it that second reading, because they delete and so stay on the origin-validated default ref; it records HEAD at check time, re-reads it under a lock concurrent git/jj writes cannot bypass (git `HEAD.lock`; jj a single `rebase`/`abandon` revset of `@ & commit_id(expected)`), and re-checks dirtiness under that lock before any destructive tree update so uncommitted file or index changes after the first dirty check are not overwritten. It skips the slot if the lock cannot be taken, HEAD changed, or the tree became dirty, and fails closed when dirty, unverifiable, or merge state cannot be proven. `return` still discards dirty trees (`requireClean=false`)
- The branch worktrees are cut from is opt-in configurable: `base_branch` in `treehouse.toml` (repo-level wins over user-level) and `treehouse get --base <branch>` per invocation, flag over config, both empty keeping today's inference. `pool.resolveBaseBranch` is the single resolution point, called AFTER the fetch so a branch that exists only on origin resolves, and its result feeds both `AddWorktree` (new slot) and `IsWorktreeSafeToReset`/`ResetWorktreeToRef` (recycled slot). Only an explicit request is verified, through `vcs.VerifyBaseBranch` → `gitvcs.BranchExists`, which matches `refs/heads/<b>` or `refs/remotes/origin/<b>` — the same two refs `branchRef` chooses between — so branch names only, never tags/SHAs/`origin/<b>`, and never the literal `HEAD` (rejected by name, because `git clone` writes `refs/remotes/origin/HEAD`). Verification is mandatory rather than cosmetic: acquire SKIPS a slot whose safety check fails, so an unverified typo would present as a pool with nothing reusable and burn a fresh slot per call instead of reporting the typo. `VerifyBaseBranch` deliberately sits outside the `Backend` interface and refuses non-git backends: the jj path is plausibly fine but was never exercised, and a `Backend` method would put an unverified implementation on the destructive path. `gitvcs.branchRef` returns FULLY QUALIFIED refs: a bare name lets git rank `refs/tags/<b>` above `refs/heads/<b>`, silently cutting the slot from a same-named tag. `WorktreeEntry.BaseBranch` records ONLY an explicitly requested base, never an inferred default: prune and destroy give a slot a second reading against that field, so recording an inferred default there would widen what they delete for pools that never opted in. `ReleaseConditional` parks returned slots on the base they were cut from, resolved as caller-supplied (the CONFIGURED base, never the invocation's `--base`, because parking is about what the next acquire can recycle) → `WorktreeEntry.BaseBranch` recorded at acquire → repository default, and it clears the field when a slot parks on the default; without the recorded fallback a pool driven only by `--base` parks every slot on the default, recycles none, and grows to `max_trees` before reporting slots `status` shows as available. The default is resolved before the state lock so its failure surfaces before `beforeReset` kills processes, but it is only fatal when there is no requested base; if the reset on the requested base fails (branch deleted after verification) release warns and parks on the default rather than stranding the reservation. `LeaseInfo.BaseBranch` reports the resolved base for every acquisition (`get --lease --json`). `status` prints the base as a human line only — `status --json` is a top-level array and must stay one. One base per slot is a SHARED invariant: acquire, parking, prune, and destroy all judge a slot against the base it was cut from. `pool.headLandedOnItsBase` gives a slot the default ref reports unmerged a second reading against `WorktreeEntry.BaseBranch` (via `vcs.BaseBranchMergeRef` → `gitvcs.BranchMergeRef`, git-only for the same reason as `VerifyBaseBranch`); without it a `base_branch = "develop"` pool has every pristine slot labelled unlanded and neither `prune --yes` nor `destroy --all --yes` can ever reclaim it. It applies only to a slot carrying an EXPLICIT base (an empty field returns immediately), which is what keeps non-opt-in pools on exactly today's deletion semantics, and it runs only AFTER the default-ref check returned a definitive "not merged", so an unverifiable slot keeps that check's fail-closed classification
- In-use detection uses process scanning plus short-lived persisted owner reservations for lifecycle operations
- Durable leases are a separate, process-independent reservation: `WorktreeEntry.Leased`/`LeaseID`/`LeaseHolder`/`LeasedAt` persist in the state file with `omitempty`. Every acquisition generates a new immutable 128-bit random `LeaseID`; older state without it loads with an empty ID and remains releasable through the legacy unconditional path. A lease is NOT derived from live processes, so it survives with zero processes inside the worktree and `healState` never clears it. Leased worktrees are skipped by `Acquire` and `prune`, classified `DestroyLeased` by destroy (removable only when the exact path is named with `--include-leased`, NEVER via `--all`), surfaced by `status` as `StatusLeased`, and cleared by `Release` (`return`)
- `destroy` is safe-by-default and mirrors `prune`: dry-run unless `--yes`, narrow explicit targets (`destroy <path>` for one worktree; `destroy <pool> --all` for that pool only - there is NO cross-pool/global destroy, and `--all` with no pool target is an error). The old blunt `--force` flag is REMOVED (this was the v2.0.0 breaking change); each risk class is its own opt-in: `--include-unlanded` (dirty, unmerged, or unverified), `--include-in-use` (running process or owner reservation; processes terminated cleanly first), `--include-leased` (leased, single named path only). A bare `--all --yes` removes only the disposable set (merged, clean, idle, unleased) and skips the rest with the flag that would include each. Bulk skips exit 0; a single-target skip exits non-zero. Entry points: `pool.DestroyWorktree` (single path, `allowLeased=true`) and `pool.DestroyPool` (bulk, `allowLeased=false`). Both share `classifyForDestroy` in `internal/pool/destroy.go`, which reuses prune's classification primitives (`ownerAlive`, `process.FindProcessesInWorktree`, `backingRepositoryMissing`, `vcs.IsDirty`, `vcs.IsHeadMergedIntoRef` against the `resolvePruneDefaultRef` ref) so destroy and prune agree on leased/in-use/unlanded/unverified/disposable. Removal keeps the same two-phase reservation as prune (reserve under flock, run `pre_destroy` hooks, remove only worktrees whose `sameDestroyReservation` still holds), so a worktree re-acquired during its hook is never deleted
- `get --lease` (see `getLeaseRunE`) is the non-interactive acquire: it opens no subshell, routes hook output and banners to stderr, and keeps path-only stdout unchanged. `get --lease --json` returns `pool.AcquireLeaseInfo`, and `status --json` exposes the same `lease_id`, holder, and timestamp. Conditional return uses `pool.ReleaseConditional` with `--if-lease-id` and optional `--if-lease-holder`; comparison, caller-side preparation, reset, and final clear share one `WithStateLock`, while return without conditions keeps the legacy path-only behavior
- Dirty checks include untracked files even when repository config hides them from normal `git status` output
- Prune deletes only idle managed worktrees that are clean and whose HEAD is merged into the default branch; dry run is the default
- Prune reports unsafe idle worktrees in grouped, stable categories and keeps raw VCS diagnostics for verbose output instead of default output
- Prune treats backing-repository-missing linked worktrees as orphans in both flavors (a git `.git` gitdir pointer or a jj `.jj/repo` store pointer naming a deleted directory; a `.jj/repo` directory is a main workspace and never an orphan); they are only deletable with explicit `--prune-orphans --yes`, and each candidate warns that content could not be verified
- Prune never treats an unreachable origin as a deletable orphan; those worktrees stay skipped because the repository may still be valid. Each backend owns its unreachable-origin error vocabulary (`gitvcs`/`jjvcs` `IsOriginAccessError`; jj shells out to git so its patterns wrap git's), and the `vcs` facade classifies by error content, not by the configured backend
- Global prune enumerates managed pool directories under the user-level treehouse root and derives each worktree's owning repository from VCS metadata instead of relying on the current directory
- Global prune loads user-level config and hooks only because it can run without a repository context
- State file tracks pool membership, temporary owner/destroy reservations, and explicit durable leases.
  It still does not infer long-term usage from processes.
- `WriteState` is atomic: it writes to a temp file in the pool directory, fsyncs it, commits it with the platform replacement primitive, and syncs the parent directory where supported.
  A crash mid-write can never leave a truncated or empty state file.
  `ReadState` treats a state file that exists but fails to parse (empty or truncated) as recoverable rather than a hard failure: it prints a loud warning to stderr and rebuilds a `State` by scanning the pool directory for worktree subdirectories still on disk (`recoverCorruptState` in `internal/pool/state.go`).
  Since the real reservation (owner vs. lease vs. idle) is unknowable from disk alone, every recovered entry is marked `Leased` with a `recoveredLeaseHolder` placeholder.
  `Acquire` and `prune` skip recovered entries, and `destroy` only removes one via a single named `--include-leased` target.
  A human clears a recovered entry with `treehouse status` then `treehouse return` (or `destroy --include-leased`) once verified
- All VCS operations go through the `internal/vcs` seam (`vcs.Backend`, 20 operations); backends shell out to the `git`/`jj` binaries (go-git has incomplete worktree support). Git is the default backend everywhere, including colocated (`.jj`+`.git`) and `.jj`-only repos; jj is a strict opt-in via `vcs = "jj"` in config or `TREEHOUSE_VCS=jj` (precedence: env, repo `treehouse.toml`, user config), effective only where a `.jj` directory exists; an unrecognized `vcs` value is ignored (git default kept) with one deduped stderr warning naming the value and its source. Pooled jj workspaces are `.jj`-only and cannot carry an untracked config file, so `backendFor` resolves their opt-in by reading the `.jj/repo` pointer and checking config at the main repository root — file inspection only, never backend selection from a marker. Per-worktree facts and actions, however, dispatch on the slot's own marker (`slotMarkerBackend`: a `.git` entry wins, then a `.jj` directory): `backendForWorktree` routes `IsDirty`, `IsHeadMergedIntoRef` (with its ref from `DefaultBranchMergeRefForWorktree`), `ResetWorktree`, `ResetWorktreeToRef`, `IsWorktreeSafeToReset`, `DetachWorktree`, `FindMainRepoRootFrom`, and release-time root/branch discovery (`DefaultBranchForWorktree`), and `backendForRemoval` routes `RemoveWorktree`/`RemoveCleanWorktree` the same way, falling back to `backendFor(repoRoot)` when the path is missing so error surfacing is unchanged. This is artifact-typed dispatch of operations on an existing slot, NOT marker-based opt-in — ordinary directories without a marker and worktree creation still follow the configured backend — so the configured backend never answers for a slot of the other flavor (a `.jj`-only slot read through git would resolve the repository enclosing the pool and report its facts, misclassifying dirty work as disposable). Acquire is flavor-aware too: `get` reuses only marker-matching slots and creates new ones with the selected backend; other-flavor slots stay untouched, are surfaced by `status` (`WorktreeStatus.Flavor`), count toward `max_trees`, and are migrated via `destroy` + re-`get`. A markerless (damaged) slot fails closed everywhere: acquire never reuses it, release clears its lease without branch discovery, reset, or detach, `status` reports it `damaged` without reading its facts, prune classifies it cannot-verify without reading facts through the fallback, and destroy classifies it unverified and removes it (plain-directory route; stale registrations self-heal at the next add) only with `--include-unlanded`; as defense in depth the destructive `vcs` wrappers (`ResetWorktree`, `ResetWorktreeToRef`, `DetachWorktree`) refuse markerless paths outright
- jj backend semantics: dirty means the working-copy commit `@` is non-empty or described; reset is `jj abandon -r @` then `jj new <default>` (recoverable via `jj op restore`); merged-detection is the ancestry revset `@- & ~::<ref>` being empty, so squash-merged work deliberately reads unmerged (fail-safe); default branch resolution prefers origin bookmarks `main`/`master`/`trunk` and never uses bare `trunk()` (it falls back to `root()` without remotes); the workspace store pointer is rewritten to a canonicalized absolute path and canonicalized again on both read routes, so a repository reached through a symlinked path resolves to one pool identity; `PruneWorktrees` is a documented no-op with self-healing at add time; as defense in depth, `RemoveWorktree` refuses to delete an existing directory that is not a jj workspace or that is the main workspace (its `.jj/repo` is the store itself, not a pointer file), so even a misrouted call cannot silently delete git-owned files or the whole repository — an already-missing path still gets its stale workspace registration forgotten. jj tests isolate `JJ_CONFIG` (with `git.colocate=false`) and skip when `jj` is not on PATH, so CI without jj is unaffected
- The pool root is made self-ignoring (`.gitignore` containing `*` written inside it) because non-colocated jj repos never read `.git/info/exclude`; inside git repos the `info/exclude` entry is still added too (`config.EnsureExcluded`)
- Self-healing: stale state entries are auto-removed, and `get` prunes stale worktree registrations before adding a worktree (both routes go through `vcs.PruneWorktreeAt`, which prunes the repository and additionally clears the slot's own registration when git left its `initializing` lock on it; plain `git worktree prune` silently skips locked registrations, so an interrupted `worktree add` would otherwise keep the slot name registered and wedge every later `get`. Only that lock reason is cleared and only while the directory is gone, so a user's lock is never disturbed. The jj backend has no such lock and forgets a stale same-path workspace registration at add time). A failed `vcs.AddWorktree` also removes the slot directory it just created and re-prunes, because `nextName` derives names from state alone: without that cleanup a half-created slot would collide with the same name on every later `get`. The cleanup is skipped when the slot directory already existed, so pre-existing content is never deleted
- Every git subprocess runs under a deadline (`gitCommandContext` in `internal/vcs/gitvcs`), so a stalled command fails with a diagnostic naming the subcommand, the working directory, and the env var that raises its budget. Two budgets, one for metadata commands and a longer one for size/network-bound commands, each overridable by an environment variable; README's "Git command timeouts" owns the defaults, the long-command list, and the variable names. `cmd.WaitDelay` also bounds a completed git whose output pipe a background helper still holds. The jj backend is not yet bounded this way

## Contribution Gate

- PRs to `main` must carry the no-mistakes pipeline signature and a v1 pipeline step attestation whose `head_sha` matches the current PR head (`review`, `test`, and `document` each `status=completed`). Signature-only bodies from no-mistakes older than 1.46.0 fail. The required check is `PR must be raised via no-mistakes` (`.github/workflows/no-mistakes-required.yml`), enforced by the repository `main` ruleset; only the owner/Admin role can bypass it.
- The required check is decided by the SHARED composite action `kunchenguid/no-mistakes/.github/actions/require-no-mistakes`, pinned to an immutable commit (never `@main`, which the judged PR could edit). Bot exemptions ride as its `exempt-authors` input, deliberately not a job-level `if:`: a skipped job never reports a required context, so the PR would block on a status that can never arrive. Bumping the pin is a separate, deliberate PR.
- `.github/scripts/no-mistakes-gate.sh` survives for ONE consumer: `release.yml`'s `release-pr-gate-status` job (see the next bullet). Its structural release-please test is therefore still load-bearing, not dead code, and `TestNoMistakesGateDecisions` drives the script directly.
- release-please opens its PRs with `GITHUB_TOKEN`, so GitHub creates **no** workflow runs on them and the gate can never report there. `release.yml`'s `release-pr-gate-status` job publishes the required context on the release PR head by running that same gate script, so release PRs go green without an owner override.
- A workflow backing a required check must never use `paths`/`paths-ignore`: a filtered required check never reports and blocks the PR forever. `TestPullRequestWorkflowsExcludeReleasePleaseOutputs` encodes both that rule and the opposite rule for ordinary PR workflows.

## Windows Compatibility

This project targets Linux, macOS, and Windows. All new code **must** work on Windows. Follow these rules:

- **Paths**: Never hardcode `/` as a path separator. Use `filepath.Join()`, `filepath.Separator`, or `filepath.ToSlash()` as appropriate.
- **Shell**: Do not assume `/bin/sh` or `$SHELL` exist. On Windows, use `%COMSPEC%` (usually `cmd.exe`). See `internal/shell/shell.go` for the pattern.
- **Syscalls**: Unix-only syscalls (e.g., `syscall.Flock`) must be isolated behind build tags (`//go:build !windows` / `//go:build windows`). See `internal/pool/lock_unix.go` and `lock_windows.go` for the pattern.
- **Build tags**: Follow the existing `_unix.go` / `_windows.go` naming convention (see also `internal/updater/sysproc_*.go`).
- **CI**: The CI matrix runs tests on `ubuntu`, `macOS`, and `windows`. Cross-compile locally with `GOOS=windows go build ./...` to catch issues early.
- **Process detection**: `gopsutil` is cross-platform - no special handling needed, but avoid importing platform-specific process APIs directly.

## Config

Place repo-safe settings in repo root `treehouse.toml` or user-level `~/.config/treehouse/config.toml`:

```toml
max_trees = 16

# Optional worktree root.
# Relative roots need a repo context; use an absolute user-level root for global prune.
# root = "$HOME/worktrees"

# Optional VCS backend. Git is the default everywhere; "jj" opts in to the
# Jujutsu backend (or per-command with TREEHOUSE_VCS=jj).
# vcs = "jj"

# User-level config only:
[hooks]
post_create = ["./scripts/setup-venv.sh"]
pre_destroy = ["./scripts/teardown.sh"]
```

Hooks are ignored in repo-level config for safety.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
