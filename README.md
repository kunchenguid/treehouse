# Treehouse

[![CI](https://github.com/atinylittleshell/treehouse/actions/workflows/ci.yml/badge.svg)](https://github.com/atinylittleshell/treehouse/actions/workflows/ci.yml)
[![Release](https://github.com/atinylittleshell/treehouse/actions/workflows/release.yml/badge.svg)](https://github.com/atinylittleshell/treehouse/actions/workflows/release.yml)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)

<h3 align="center">Never stop prompting.</h3>

Run parallel AI coding agents on the same repo without conflicts.
Treehouse maintains a pool of reusable, isolated worktrees so each of your agents gets its own environment instantly — no cloning, no conflicts, no coordination overhead.

```sh
$ treehouse                    # get a worktree and drop into a subshell
✓ Entered worktree at ~/.treehouse/myproject-a1b2c3/1/myproject
  (detached at origin/main). Type 'exit' to return.

# You're now in an isolated worktree.
# Run your AI agent, make changes, do whatever you need.

$ exit                         # auto-return when done
Worktree returned to pool.
```

## Install

**macOS / Linux**

```sh
curl -fsSL https://atinylittleshell.github.io/treehouse/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://atinylittleshell.github.io/treehouse/install.ps1 | iex
```

**Go**

```sh
go install github.com/atinylittleshell/treehouse@latest
```

**From source**

```sh
git clone https://github.com/atinylittleshell/treehouse.git
cd treehouse
make build
./treehouse
```

## How It Works

Treehouse manages a **pool of git worktrees** per repository, stored under `~/.treehouse/`.

- **Detached HEAD** — worktrees are checked out at `origin/main` in detached HEAD mode, avoiding branch name conflicts entirely.
- **No daemon** — all operations are inline CLI commands. No background processes, no port conflicts, no state to get corrupted.
- **Self-healing** — stale state entries (from crashed shells or killed processes) are automatically cleaned up on the next operation.
- **In-use detection** — treehouse scans running processes to determine which worktrees are active in-use. Usage state is never cached, so it's always accurate.

## CLI Reference

| Command                    | Description                                          |
| -------------------------- | ---------------------------------------------------- |
| `treehouse`                | Get a worktree and open a subshell (alias for `get`) |
| `treehouse get`            | Acquire a worktree from the pool                     |
| `treehouse status`         | Show pool status (name, path, status)                |
| `treehouse return [path]`  | Return a worktree to the pool                        |
| `treehouse destroy [path]` | Remove a worktree from the pool                      |
| `treehouse init`           | Create a default `treehouse.toml` config file        |
| `treehouse update`         | Update treehouse to the latest version               |

### Flags

| Command   | Flag      | Description                       |
| --------- | --------- | --------------------------------- |
| `return`  | `--force` | Skip dirty-check prompt           |
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
