# Treehouse - Agent Guide

## What is this?

Treehouse is a Go CLI tool that manages a pool of git worktrees for parallel AI coding agent workflows. It maintains reusable, pre-warmed worktrees so agents get isolated environments instantly.

## Project Structure

- `main.go` - entry point, calls `cmd.Execute()`
- `cmd/` - CLI commands (cobra): `get` (incl. `get --lease`), `return`, `status`, `prune`, `destroy`
- `internal/config/` - config file loading (`treehouse.toml`)
- `internal/hooks/` - user-configured lifecycle hook command execution
- `internal/pool/` - pool manager (acquire, release, list, destroy, prune) + state file
- `internal/git/` - git operations (shells out to `git` binary)
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
- Pool identity is keyed off the repository's **common git dir** (`git.CommonGitDir`), which is shared by every linked worktree and the bare repo itself - NOT off the current working tree. So all worktrees of one repo map to a single shared pool instead of one pool per checkout. `config.ResolvePoolDir` resolves the common git dir from whatever directory it is handed, so `get`/`status`/`prune`/`return`/`destroy` all converge on the same pool name regardless of which worktree (or the bare repo) they run in. The pool name is `<repoName>-<hash>`: `repoName` comes from `git.RepoNameFromCommonDir` (a `.git`/`.bare` marker yields its parent dir's name; a standalone bare dir yields its own basename minus a `.git` suffix), and `hash` is the origin remote URL when present, falling back to `git.MainRootFromCommonDir` (the main repo root, stable across worktrees) for local-only repos. The pool *root* (parent dir) is still anchored on the passed-in dir, so relative `root` config stays repo-context dependent. NOTE on upgrade: this changed pool identity from the old per-worktree scheme. Single-checkout repos are unaffected and keep their existing pool - remote repos because the hash is still the remote URL, local-only repos because the main-root hash reproduces the old worktree-toplevel value. Repos used from multiple differently-named checkouts orphan their old per-checkout pools (clean up with `prune`/`destroy`)
- `get`/`status`/`prune` resolve their working directory via `git.ResolveWorkDir` (the working-tree root when one exists, else the common git dir), so they run from a bare repo or a gitdir-file parent (e.g. the `.bare` layout), not only from a checkout. Worktree-creating git ops (`AddWorktree`, `GetDefaultBranch`, `Fetch`) run fine from a bare dir. `init` still requires a working tree (it scaffolds repo-level config)
- Detached HEAD worktrees reset to whichever of local or origin default branch is further ahead (prefers origin on divergence). A new worktree always starts detached at the default branch tip - it never inherits the branch of the checkout `get` was invoked from
- In-use detection uses process scanning plus short-lived persisted owner reservations for lifecycle operations
- Durable leases are a separate, process-independent reservation: `WorktreeEntry.Leased`/`LeaseHolder`/`LeasedAt` persist in the state file (all `omitempty`, so pre-lease state files keep today's behavior). A lease is NOT derived from live processes, so it survives with zero processes inside the worktree and `healState` never clears it (it only clears dead owner reservations). Leased worktrees are skipped by `Acquire` and `prune`, classified `DestroyLeased` by destroy (removable only when the exact path is named with `--include-leased`, NEVER via `--all`), surfaced by `status` as `StatusLeased`, and cleared by `Release` (`return`)
- `destroy` is safe-by-default and mirrors `prune`: dry-run unless `--yes`, narrow explicit targets (`destroy <path>` for one worktree; `destroy <pool> --all` for that pool only - there is NO cross-pool/global destroy, and `--all` with no pool target is an error). The old blunt `--force` flag is REMOVED (this was the v2.0.0 breaking change); each risk class is its own opt-in: `--include-unlanded` (dirty, unmerged, or unverified), `--include-in-use` (running process or owner reservation; processes terminated cleanly first), `--include-leased` (leased, single named path only). A bare `--all --yes` removes only the disposable set (merged, clean, idle, unleased) and skips the rest with the flag that would include each. Bulk skips exit 0; a single-target skip exits non-zero. Entry points: `pool.DestroyWorktree` (single path, `allowLeased=true`) and `pool.DestroyPool` (bulk, `allowLeased=false`). Both share `classifyForDestroy` in `internal/pool/destroy.go`, which reuses prune's classification primitives (`ownerAlive`, `process.FindProcessesInWorktree`, `backingRepositoryMissing`, `git.IsDirty`, `git.IsHeadMergedIntoRef` against the `resolvePruneDefaultRef` ref) so destroy and prune agree on leased/in-use/unlanded/unverified/disposable. Removal keeps the same two-phase reservation as prune (reserve under flock, run `pre_destroy` hooks, remove only worktrees whose `sameDestroyReservation` still holds), so a worktree re-acquired during its hook is never deleted
- `get --lease` (see `getLeaseRunE`) is the non-interactive acquire: it implies the durable lease, opens no subshell, routes post-create hook stdout and all banners to stderr, and prints ONLY the worktree path to stdout so `path=$(treehouse get --lease)` is clean. `--lease-holder`/`$TREEHOUSE_LEASE_HOLDER` set the recorded holder. `pool.AcquireLease` is the entry point; both it and `Acquire` delegate to the shared `acquire(..., acquireOptions)` core, and `markAcquired` stamps either a lease or an owner reservation. Concurrency safety comes from the existing `WithStateLock` (flock) around all pool mutation
- Dirty checks include untracked files even when repository config hides them from normal `git status` output
- Prune deletes only idle managed worktrees that are clean and whose HEAD is merged into the default branch; dry run is the default
- Prune reports unsafe idle worktrees in grouped, stable categories and keeps raw git diagnostics for verbose output instead of default output
- Prune treats backing-repository-missing linked worktrees as orphans; they are only deletable with explicit `--prune-orphans --yes`, and each candidate warns that content could not be verified
- Prune never treats an unreachable origin as a deletable orphan; those worktrees stay skipped because the repository may still be valid
- Global prune enumerates managed pool directories under the user-level treehouse root and derives each worktree's owning repository from git metadata instead of relying on the current directory
- Global prune loads user-level config and hooks only because it can run without a repository context
- State file tracks pool membership, temporary owner/destroy reservations, and explicit durable leases.
  It still does not infer long-term usage from processes.
- Git operations shell out to `git` (go-git has incomplete worktree support)
- Self-healing: stale state entries are auto-removed

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

# User-level config only:
[hooks]
post_create = ["./scripts/setup-venv.sh"]
pre_destroy = ["./scripts/teardown.sh"]
```

Hooks are ignored in repo-level config for safety.
