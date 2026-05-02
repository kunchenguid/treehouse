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

Treehouse manages a **pool of git worktrees** per repository, stored under `~/.treehouse/`.

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
  ┌────────────────────────────────────┐
  │  Scan pool for available worktree  │
  │  (not in-use, not dirty)           │
  └──────────┬─────────────────────────┘
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
- **In-use detection** — treehouse scans running processes to determine which worktrees are in-use. Usage state is never persisted, so it's always accurate.

## CLI Reference

| Command                    | Description                                          |
| -------------------------- | ---------------------------------------------------- |
| `treehouse`                | Get a worktree and open a subshell (alias for `get`) |
| `treehouse get`            | Acquire a worktree from the pool                     |
| `treehouse status`         | Show pool status (highlights your current worktree)  |
| `treehouse return [path]`  | Terminate lingering worktree processes and return it to the pool |
| `treehouse destroy [path]` | Remove a worktree from the pool                      |
| `treehouse init`           | Create a default `treehouse.toml` config file        |
| `treehouse update`         | Update treehouse to the latest version               |

### Flags

| Command   | Flag      | Description                       |
| --------- | --------- | --------------------------------- |
| `return`  | `--force` | Clean, reset, and return without prompting |
| `destroy` | `--force` | Force destroy even if in-use      |
| `destroy` | `--all`   | Destroy all worktrees in the pool |

## Configuration

Create a config file with `treehouse init`, or add one manually:

**Repo-level:** `treehouse.toml` in the repository root

**User-level:** `~/.config/treehouse/config.toml`

```toml
# Maximum number of worktrees in the pool
max_trees = 16
```

The repo-level config takes precedence. If no config is found, the default pool size is 16.

## Development

```sh
make build          # Build the binary
make test           # Run tests
make lint           # Run gofmt + go vet
make dist           # Cross-compile for all platforms
make install        # Install to $GOPATH/bin or /usr/local/bin
make clean          # Remove build artifacts
```
