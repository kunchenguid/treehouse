# Treehouse - Agent Guide

Treehouse is a Go CLI that manages a pool of reusable git worktrees (or, opt-in, Jujutsu workspaces) so parallel AI coding agents get isolated environments instantly.
User-facing behavior: [README.md](README.md). Settings and precedence: [treehouse.toml.example](treehouse.toml.example). Invariants and their rationale: [docs/design.md](docs/design.md) - read the entry for an area before changing it.

## Layout

- `cmd/` - cobra commands; exit statuses are mapped in `cmd/exit.go`
- `internal/pool/` - acquire, release, lease, list, prune, destroy, and the state file (`state.go` is the sole authority for where a slot lives)
- `internal/vcs/` - the `vcs.Backend` seam; `gitvcs/` and `jjvcs/` shell out to `git`/`jj`
- `internal/config/`, `internal/hooks/`, `internal/process/`, `internal/shell/`, `internal/ui/`

## Commands

- `make build`, `make test` (`go test ./...`), `make lint`
- `GOOS=windows go build ./...` before shipping; CI tests on Linux, macOS, and Windows

## Invariants

- Acquire reuse, prune, and destroy fail closed on unproven dirtiness, merge state, ownership, or slot marker; `return` may reset a dirty tree after confirmation.
- No daemon; state tracks membership, short-lived reservations, and durable leases, not process-derived long-term usage.
- Only `return` clears ordinary leases. Recovered leases are auto-freed only when process-free, tracked-clean, and landed on a remote-tracking ref or slot base; on Unix, untracked files are moved into a kept backup beside the pool; on Windows they keep the slot leased. Unsafe or unverifiable recoveries remain leased; `status` explains how to inspect and return by name. Unknown seeded ignored files may remain when reused; `return --all` skips quarantined entries.
- All VCS operations go through `internal/vcs`; per-slot operations dispatch on the slot's own marker, never the configured backend. Git is the default; jj is strict opt-in.
- One base branch per slot is shared by acquire, parking, prune, and destroy; `WorktreeEntry.BaseBranch` records only an explicitly requested base.
- Prune and destroy are dry-run unless `--yes`; each risk class needs its own opt-in flag, and there is no cross-pool destroy.
- A command that leaves the world unchanged must not exit 0 (see `cmd/exit.go` and docs/design.md for the two exceptions).
- `WriteState` stays atomic; a corrupt state file recovers every entry as leased and quarantined.
- Lifecycle hooks are read from user-level config only; repo-level hooks are ignored for safety.

## Windows compatibility

All code must work on Windows:

- Use `filepath` helpers, never a hardcoded `/` separator.
- Do not assume `/bin/sh` or `$SHELL`; follow `internal/shell/shell.go` (`%COMSPEC%` on Windows).
- Isolate Unix-only syscalls behind `_unix.go`/`_windows.go` build tags (see `internal/pool/lock_*.go`).
- Use `gopsutil` for process data, not platform-specific process APIs.

## Contribution gate

- PRs to `main` must be raised through no-mistakes (required check `PR must be raised via no-mistakes`); see [CONTRIBUTING.md](CONTRIBUTING.md).
- A workflow backing a required check must never use `paths`/`paths-ignore`. The gate action pin and `.github/scripts/no-mistakes-gate.sh` are load-bearing; see docs/design.md before touching CI.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
