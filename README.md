<h1 align="center">treehouse</h1>

<p align="center">
  <a href="https://github.com/kunchenguid/treehouse/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/kunchenguid/treehouse/ci.yml?style=flat-square&label=CI" /></a>
  <a href="https://github.com/kunchenguid/treehouse/actions/workflows/release.yml"><img alt="Release" src="https://img.shields.io/github/actions/workflow/status/kunchenguid/treehouse/release.yml?style=flat-square&label=Release" /></a>
  <a href="#"><img alt="Platform" src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue?style=flat-square" /></a>
  <a href="https://x.com/kunchenguid"><img alt="X" src="https://img.shields.io/badge/X-@kunchenguid-black?style=flat-square" /></a>
  <a href="https://discord.gg/BW4aJuQhTf"><img alt="Discord" src="https://img.shields.io/discord/1439901831038763092?style=flat-square&label=discord" /></a>
</p>

<h3 align="center">Manage worktrees without managing worktrees.</h3>

Are you still only working on one task at a time? Are you manually juggling between a few clones of the same repo?

Or... are you starting a new worktree for every agent session, losing all your installed dependencies and build cache each time, and wondering why your agents are slow?

<p align="center">
  <img src="https://raw.githubusercontent.com/kunchenguid/treehouse/main/demo.gif" alt="treehouse demo" width="800" />
</p>

Treehouse helps you manage a pool of reusable, isolated worktrees so each of your agents gets its own environment instantly — no cloning, no conflicts, no coordination overhead.

- **Instant isolation** — `treehouse` puts you into a clean worktree with zero hassel.
- **Reusable worktrees** — worktrees are preserved in a pool when you're done, with dependencies and build cache intact, ready for the next agent.
- **Conflict-free** — automatic detection of in-use worktrees and your agents never step on each other's toes.

## Quick Start

```sh
$ cd myproject                 # start in your repo as usual
$ treehouse                    # get a worktree and drop into a subshell
🌳 Entered worktree at ~/.treehouse/myproject-a1b2c3/1/myproject. Type 'exit' to return.

# You're now in an isolated worktree.
# Run your AI agent, make changes, do whatever you need.

$ exit                         # exit the subshell when you're done
🌳 Terminated lingering processes: opencode (pid 12345)
🌳 Worktree returned to pool.
```

## Install

**macOS / Linux**

```sh
curl -fsSL https://kunchenguid.github.io/treehouse/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://kunchenguid.github.io/treehouse/install.ps1 | iex
```

**Nix**

```sh
nix run github:kunchenguid/treehouse
```

Or add to your flake inputs:

```nix
treehouse = {
  url = "github:kunchenguid/treehouse";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

**Go**

```sh
go install github.com/kunchenguid/treehouse@latest
```

**From source**

```sh
git clone https://github.com/kunchenguid/treehouse.git
cd treehouse
make install
```

## How It Works

Treehouse manages a **pool of git worktrees** per repository, stored under the configured treehouse root.
The default treehouse root is `~/.treehouse/`.

```
  treehouse
      │
      ▼
  Find repo root
      │
      ▼
  git fetch origin
      │
      ▼
  ┌───────────────────────────────────────┐
  │  Scan pool for available worktree     │
  │  (not leased, not in-use, not dirty)  │
  └──────────┬────────────────────────────┘
             │
        ┌────┴────┐
        │  Found? │
        └────┬────┘
         yes/ \no
           /   \
          ▼     ▼
   Reset to   Create new worktree
   latest     (detached HEAD at
   default    latest default
   branch     branch)
              & add to pool
          \   /
           \ /
            ▼
  Spawn subshell in worktree
  (agent works here)
           │
           ▼
     exit subshell
           │
           ▼
  Terminate lingering worktree
  processes, reset worktree,
  & return to pool
  (ready for next agent)
```

- **Detached HEAD** — worktrees use detached HEAD mode, reset to whichever of the local or remote default branch is further ahead, avoiding branch name conflicts entirely.
- **No daemon** — all operations are inline CLI commands. No background processes, no state to get corrupted.
- **In-use detection** — treehouse scans running processes and short-lived owner reservations to determine which worktrees are in-use. Reservations are persisted only while `get`, `destroy`, and `prune` lifecycle work is running.
- **Durable leases** — `treehouse get --lease` reserves a worktree as a persistent home without keeping a process inside it. The lease is recorded in treehouse's own state, so the worktree is never handed out by a later `get` and never removed by `prune` until you release it with `treehouse return`. Unlike process-based in-use detection, a lease survives with zero processes running inside the worktree.
- **Dirty detection** - treehouse treats tracked changes and untracked files as dirty, even when repository config hides untracked files from normal `git status` output.
- **Safe pruning** - By default, `treehouse prune` removes only idle managed worktrees whose HEAD is already merged into the default branch and whose working tree is clean.
  `treehouse prune --all` applies the same safety checks across every managed pool under the user-level treehouse root.
  Backing-repository-missing orphans are reported by default; `--prune-orphans` includes them as unverified prune candidates, and `--yes` is required before deletion.
  It is a dry run unless you pass `--yes`.

## CLI Reference

| Command                    | Description                                          |
| -------------------------- | ---------------------------------------------------- |
| `treehouse`                | Get a worktree and open a subshell (alias for `get`) |
| `treehouse get`            | Acquire a worktree from the pool                     |
| `treehouse get --lease`    | Durably lease a worktree without a subshell; print its path |
| `treehouse status`         | Show pool status (highlights leased and current worktrees) |
| `treehouse return [path]`  | Release any lease, terminate lingering worktree processes, and return it to the pool |
| `treehouse prune`          | Dry-run removal of stale idle worktrees in the current repo pool |
| `treehouse prune --all`    | Dry-run removal of stale idle worktrees across every managed pool |
| `treehouse destroy [path]` | Remove a worktree from the pool                      |
| `treehouse init`           | Create a default `treehouse.toml` config file        |
| `treehouse update`         | Update treehouse to the latest version               |

### Flags

| Command   | Flag      | Description                       |
| --------- | --------- | --------------------------------- |
| `get`     | `--lease` | Durably lease the worktree without opening a subshell; print only its path to stdout |
| `get`     | `--lease-holder` | Optional label recorded as the lease holder (defaults to `$TREEHOUSE_LEASE_HOLDER`) |
| `return`  | `--force` | Clean, reset, and return without prompting |
| `prune`   | `--yes`   | Delete listed prune candidates instead of doing a dry run |
| `prune`   | `--all`   | Sweep every managed pool under the user-level treehouse root |
| `prune`   | `--global` | Alias for `--all` |
| `prune`   | `--prune-orphans` | Include backing-repository-missing orphans in prune candidates |
| `prune`   | `--verbose`, `-v` | Show detailed skip diagnostics |
| `destroy` | `--force` | Force destroy even if in-use or leased |
| `destroy` | `--all`   | Destroy all worktrees in the pool |

### Leasing a worktree (no subshell)

`treehouse get` normally opens an interactive subshell whose lifetime is the hold: when the shell exits, the worktree returns to the pool.
That is awkward for callers that need a worktree to persist as a permanent home with no long-lived process inside it.

`treehouse get --lease` is the non-interactive, durable alternative:

```sh
path=$(treehouse get --lease)
# $path is the leased worktree's absolute path; all banners went to stderr.
```

It acquires a worktree exactly like `get`, but instead of opening a subshell it marks the worktree **leased** in treehouse's persistent state and prints only the worktree's absolute path to stdout (every human-facing message goes to stderr, so command substitution stays clean).

A leased worktree is never handed out by a later `get` and never removed by `prune`, regardless of whether any process runs inside it, until the lease is explicitly released.
It also requires `--force` to `destroy`.

Pass `--lease-holder <label>` (or set `$TREEHOUSE_LEASE_HOLDER`) to record who holds the lease; `treehouse status` then shows it next to the `leased` state.

Release a lease with `treehouse return <path>`, which clears the lease, terminates any lingering processes, resets the worktree, and returns it to the pool.
When you pass an explicit path, `treehouse return` can run from outside the repository because it resolves the managed pool from that worktree path.

### Pruning stale worktrees and orphans

`treehouse prune` is a dry run by default.
By default, it lists stale idle managed worktrees that would be deleted and shows the reclaimable disk space.
Pass `treehouse prune --yes` to delete those worktrees.

By default, prune only inspects the current repository's pool and must be run inside a git repo.
Pass `treehouse prune --all` or `treehouse prune --global` to inspect every managed pool under the user-level treehouse root from any directory.
Global prune reads the user-level config and hooks, derives each worktree's owning repository from git metadata, then fetches and checks merge safety against that repository.
Without `--prune-orphans`, pass `treehouse prune --all --yes` to delete only the globally safe stale candidates.

Prune ignores worktrees that are currently in use, leased, or reserved by another lifecycle operation.
It skips idle worktrees that are unsafe to remove and prints the skip reason, such as uncommitted tracked or untracked changes, or a HEAD commit that is not merged into the default branch.
Skip output is grouped by reason so large global sweeps stay scannable.
When `origin` exists, prune fetches it and proves each HEAD against the current remote default branch tracking ref.
Without `origin`, prune uses the local default branch ref.
If `origin` cannot be reached, prune reports `origin unreachable (cannot verify)` and leaves the worktree untouched, even when `--prune-orphans` is set.
If a linked worktree points at a missing backing repository, prune reports `orphaned (backing repository missing)`.
Plain `treehouse prune` and `treehouse prune --all` never delete those orphans.
Pass `--prune-orphans` to include true backing-repository-missing orphans in the dry run, then add `--yes` to delete them.
Treehouse cannot verify orphan contents after the backing git metadata is gone, so each orphan candidate is marked `content could not be verified`.
Use `--verbose` to show the underlying git diagnostic details for skipped worktrees.

## Configuration

Create a repo config file with `treehouse init`, or add one manually:

**Repo-level:** `treehouse.toml` in the repository root

**User-level:** `~/.config/treehouse/config.toml`

```toml
# Maximum number of worktrees in the pool
max_trees = 16

# Optional worktree root directory.
# Empty uses $HOME/.treehouse.
# Relative paths are resolved from the repo root for repo-scoped commands.
# Use an absolute user-level root for treehouse prune --all.
# root = "$HOME/worktrees"
```

The repo-level config takes precedence for repo-safe settings.
`treehouse prune --all` can run without a repository, so it uses only the user-level config and does not read per-repo `treehouse.toml` files while sweeping.
If no config is found, the default pool size is 16.

### Hooks

You can run commands automatically at worktree lifecycle points by adding a `[hooks]` section to the user-level config at `~/.config/treehouse/config.toml`.
Hooks in repo-level `treehouse.toml` are ignored for safety.

```toml
[hooks]
post_create = ["./scripts/setup-venv.sh"]
pre_destroy = ["./scripts/teardown.sh"]
```

- `post_create` runs after a worktree is provisioned or reset and right before `treehouse get` hands it to you.
  For `treehouse get --lease`, stdout from `post_create` is routed to stderr so stdout remains the leased path.
- `pre_destroy` runs before a worktree is removed by `treehouse destroy`, `treehouse destroy --all`, or prune deletion commands such as `treehouse prune --yes` and `treehouse prune --prune-orphans --yes`.

Commands in each list run sequentially in the worktree directory, via the OS shell (`/bin/sh -c` on Linux/macOS, `%COMSPEC% /c` on Windows).
If a command exits non-zero, treehouse logs the command, exit code, and stderr, then continues with the remaining commands.
A failing hook does not fail the overall `get`, `destroy`, or `prune` operation.

## Development

```sh
make build          # Build the binary
make test           # Run tests
make lint           # Run gofmt + go vet
make dist           # Cross-compile for all platforms
make install        # Install to $GOPATH/bin or /usr/local/bin
make clean          # Remove build artifacts
```
