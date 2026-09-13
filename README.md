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
# or pin a specific release tag:
nix run github:kunchenguid/treehouse/v2.3.0
```

Install into your Nix profile:

```sh
nix profile add github:kunchenguid/treehouse
```

Or add to your flake inputs:

```nix
treehouse = {
  url = "github:kunchenguid/treehouse";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

The flake exposes `#default` and `#treehouse` package outputs, plus `apps` for `nix run`.

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
You can instead keep the pool [inside the project](#in-project-storage) with `--root .`, so it lives next to the code and is removed with the project.

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
  ┌──────────────────────────────────────────────────────┐
  │  Scan pool for a safely reusable worktree            │
  │  (idle, unleased, clean, and HEAD merged into the    │
  │  exact reset target; skip if safety is unprovable)   │
  └──────────┬───────────────────────────────────────────┘
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
  processes and verify none remain
           │
           ▼
  Reset worktree & return to pool
  (ready for next agent)
```

- **Detached HEAD** — worktrees use detached HEAD mode, reset to whichever of the local or remote default branch is further ahead, avoiding branch name conflicts entirely.
- **Choosable base branch** — set `base_branch` in `treehouse.toml`, or pass `treehouse get --base <branch>`, to cut worktrees from a branch other than the repository default. Opt-in; unset keeps today's inference. Worktrees stay in detached HEAD — this selects the commit they start at, it does not create or check out a branch.
- **Choosable worktree path** — set `worktree_path` in `treehouse.toml`, or pass `treehouse get --worktree-path '<template>'`, to place new worktrees somewhere a tool requires instead of `{pool}/{slot}/{repo}`. Opt-in, and creation-only: worktrees already in the pool keep their recorded paths. See [Worktree path](#worktree-path).
- **No daemon** - all operations are inline CLI commands.
  Pool state is a small on-disk file, written under a lock by each command.
- **Interactive shell setup** — when opening a subshell on macOS or Linux, `treehouse`, `treehouse get`, and `treehouse enter` start `$SHELL` as an interactive login shell when it resolves to `bash`, `fish`, or `zsh`. Other shells, fallback shells, and Windows use their default invocation. A regular executable that is merely named like a supported shell but does not accept `-i -l` (for example a wrapper script at `/opt/tools/bash`) is an accepted limitation: the resolved basename is the contract, and PATH-identity probing would reject genuine second installs of the same shell.
- **In-use detection** — treehouse scans running processes and short-lived owner reservations to determine which worktrees are in-use. Reservations are persisted only while `get`, `destroy`, and `prune` lifecycle work is running.
- **Durable leases** - `treehouse get --lease` reserves a worktree as a persistent home without keeping a process inside it. Each acquisition gets an immutable random lease identity, and the lease is recorded in treehouse's own state. The worktree is never handed out by a later `get` and never removed by `prune` until you release it with `treehouse return`. Unlike process-based in-use detection, a lease survives with zero processes running inside the worktree.
- **State recovery** - treehouse writes pool state atomically via a temp file and replacement.
  If an existing state file is empty, truncated, or omits an on-disk worktree, treehouse rebuilds the missing entries and quarantines them for inspection and explicit destruction. See [Recovering missing pool state](#recovering-missing-pool-state).
- **Gitignored file seeding** — commit a `.worktreeinclude` file for the default selection, or pass `get --include-file <path>` for a personal manifest. Selected local files are copied from the main checkout on each acquire. See [Seeding gitignored files](#seeding-gitignored-files).
- **Dirty detection** - treehouse treats tracked changes and untracked files as dirty, even when repository config hides untracked files from normal `git status` output.
- **Safe pruning** - By default, `treehouse prune` removes only idle managed worktrees whose HEAD is already merged into the default branch and whose working tree is clean.
  `treehouse prune --all` applies the same safety checks across every managed pool under the user-level treehouse root.
  Backing-repository-missing orphans are reported by default; `--prune-orphans` includes them as unverified prune candidates, and `--yes` is required before deletion.
  It is a dry run unless you pass `--yes`.
- **Self-healing get** - `treehouse get` prunes stale git worktree bookkeeping (e.g. left behind by a crashed or forcibly removed worktree) before adding a new worktree, so a prunable registration never wedges the pool with a "missing but already registered worktree" error.

## CLI Reference

| Command                    | Description                                          |
| -------------------------- | ---------------------------------------------------- |
| `treehouse`                | Get a worktree and open a subshell (alias for `get`) |
| `treehouse get`            | Acquire a worktree from the pool                     |
| `treehouse get --lease`    | Durably lease a worktree without a subshell; print its path |
| `treehouse lease <name>`   | Durably lease an existing pool worktree in place, without touching its files or git state |
| `treehouse enter <name>`   | Open a subshell in an existing worktree by name (the number from `status`), even if it is in use; pool state is left untouched |
| `treehouse status`         | Show pool status (highlights leased and current worktrees) |
| `treehouse return [path]`  | Release any lease and return a worktree only after verifying foreign processes stopped |
| `treehouse prune`          | Dry-run removal of stale idle worktrees in the current repo pool |
| `treehouse prune --all`    | Dry-run removal of stale idle worktrees across every managed pool |
| `treehouse destroy <path>` | Dry-run removal of one worktree (safe by default; `--yes` to execute) |
| `treehouse destroy <pool> --all` | Dry-run removal of every disposable worktree in that pool |
| `treehouse init`           | Create a default `treehouse.toml` config file        |
| `treehouse update`         | Update treehouse to the latest version               |

### Flags

| Command   | Flag      | Description                       |
| --------- | --------- | --------------------------------- |
| `get`     | `--lease` | Durably lease the worktree without opening a subshell; print only its path to stdout |
| `get`     | `--lease-holder` | Optional label recorded as the lease holder (defaults to `$TREEHOUSE_LEASE_HOLDER`) |
| `get`     | `--json` | Print `path`, `lease_id`, `lease_holder`, `leased_at`, and `base_branch` as JSON (requires `--lease`) |
| `get`     | `--base` | Branch to cut this worktree from, overriding `base_branch` in config |
| `get`     | `--include-file` | Replace committed `.worktreeinclude` for this acquisition with the supplied manifest |
| `get`     | `--worktree-path` | Template for a newly created worktree's directory, overriding `worktree_path` in config |
| `lease`   | `--lease-holder` | Optional label recorded as the lease holder (defaults to `$TREEHOUSE_LEASE_HOLDER`) |
| `lease`   | `--json` | Print `path`, `lease_id`, `lease_holder`, `leased_at`, and `base_branch` as JSON (`base_branch` is best-effort: empty when the slot records no explicit base and its own worktree cannot resolve a default) |
| `enter`   | `--print-path` | Print only the worktree's absolute path to stdout instead of opening a subshell (for `cd "$(treehouse enter --print-path 1)"`) |
| `status`  | `--json` | Print worktree status and lease metadata as JSON |
| `return`  | `--force` | Clean, reset, and return without prompting |
| `return`  | `--if-lease-id` | Return only if the current lease has the expected per-acquisition identity |
| `return`  | `--if-lease-holder` | Return only if the current lease has the expected holder |
| `prune`   | `--yes`   | Delete listed prune candidates instead of doing a dry run |
| `prune`   | `--all`   | Sweep every managed pool under the user-level treehouse root |
| `prune`   | `--global` | Alias for `--all` |
| `prune`   | `--prune-orphans` | Include backing-repository-missing orphans in prune candidates |
| `prune`   | `--verbose`, `-v` | Show detailed skip diagnostics |
| `destroy` | `--all`   | Remove all worktrees in the named pool (requires a pool path) |
| `destroy` | `--yes`   | Execute the removal instead of doing a dry run |
| `destroy` | `--include-unlanded` | Also remove dirty, unmerged, or unverified worktrees (irreversible data loss) |
| `destroy` | `--include-in-use` | Also remove worktrees with a running process or owner reservation (processes are terminated cleanly first) |
| `destroy` | `--include-leased` | Also remove a leased worktree; only when the exact path is named, never via `--all` |

### Seeding gitignored files

Commit a `.worktreeinclude` file to seed selected gitignored files from the main checkout into each acquired Git worktree or jj workspace. This is useful for local configuration or generated files that every worktree needs but Git should not track. By default, only the committed file at the worktree's HEAD is used; a dirty or untracked `.worktreeinclude` in the main checkout is ignored, and a missing committed file is a no-op.

Manifests use `.gitignore` pattern syntax. A file must be ignored by the repository and selected by the manifest; tracked files and unignored untracked files are never copied. Use `!` to exclude a broader match:

```gitignore
.env*
!.env.local
local-config/
```

For a personal selection that does not need to be committed, pass an explicit manifest:

```sh
treehouse get --include-file ./personal.include
treehouse get --lease --include-file ./personal.include
```

The supplied manifest **replaces**, rather than extends, committed `.worktreeinclude` for this acquisition, on both new and reused slots. The manifest may be tracked, untracked, or ignored. Relative manifest paths resolve from your current directory; patterns inside the file always select files relative to the main checkout root, not the manifest's directory. There is no config setting or environment variable for this override.

Treehouse reads the supplied file once before acquisition. A missing or unreadable file is an error before any slot is created or reset, with no fallback to the committed manifest. An empty file seeds nothing. Later acquisitions without the flag use the committed default again. Return and reuse remove previously seeded files using Treehouse's recorded inventory, even if the local manifest has changed or been deleted.

Treehouse refreshes selected files whenever it creates or reuses a worktree. On Unix-like systems, it preserves regular-file permissions, including executable bits. A source symlink becomes a regular file containing the symlink target text; Treehouse never follows it or creates a destination symlink. Rooted filesystem operations prevent selected paths and existing destination symlinks from escaping either checkout.

If seeding fails, acquisition fails too. A newly created worktree is removed; if cleanup fails, or if a reused worktree was only partly refreshed, Treehouse records it as leased and quarantined so a later `get` cannot hand it out silently. Inspect it with `treehouse status`. If Treehouse reports that its seeded-file inventory is unknown, remove it with `treehouse destroy <path> --include-leased --yes`; `treehouse return` refuses to reuse it. Other quarantined worktrees can be returned after they are safe to reuse.

### Leasing a worktree (no subshell)

`treehouse get` normally opens an interactive subshell whose lifetime is the hold: when the shell exits, the worktree returns to the pool.
That is awkward for callers that need a worktree to persist as a permanent home with no long-lived process inside it.

`treehouse get --lease` is the non-interactive, durable alternative:

```sh
path=$(treehouse get --lease)
# $path is the leased worktree's absolute path; all banners went to stderr.
```

It acquires a worktree exactly like `get`, but instead of opening a subshell it marks the worktree **leased** in treehouse's persistent state. By default it prints only the worktree's absolute path to stdout; `--json` prints the lease allocation instead. Every human-facing message goes to stderr, so either output mode stays clean.

A leased worktree is never handed out by a later `get` and never removed by `prune`, regardless of whether any process runs inside it, until the lease is explicitly released.
A bulk `treehouse destroy <pool> --all` never removes it either; only naming its exact path with `treehouse destroy <path> --include-leased --yes` will.

Pass `--lease-holder <label>` (or set `$TREEHOUSE_LEASE_HOLDER`) to record who holds the lease; `treehouse status` then shows it next to the `leased` state.

`get --lease` can only protect a worktree it acquires itself. To give a worktree that already exists - a long-lived home acquired with plain `get`, or any registered slot that predates leases - the same protection after the fact, lease it in place:

```sh
treehouse lease 3 --lease-holder secondmate-home
```

`lease` is state-only: it never resets, fetches, cleans, or checks out the worktree, so it is safe on a slot holding live work. It refuses when the name is unknown, when the registered worktree's directory no longer exists, when the slot is being destroyed, or when it is already leased (naming the holder). Leasing a worktree whose `treehouse get` shell is still running is safe: when that shell exits, `get` sees the slot is no longer its own and leaves it untouched. `treehouse return <path>` releases an in-place lease exactly like an acquired one.

Every acquisition receives a new random `lease_id`, including reacquiring the same path with the same holder. Automation can request a stable machine-readable allocation:

```sh
treehouse get --lease --lease-holder automation-A --json
# {"path":"...","lease_id":"...","lease_holder":"automation-A","leased_at":"...","base_branch":"main"}
```

Callers that already fetched the required refs can avoid another network operation with `--no-fetch`:

```sh
git fetch origin main refs/pull/123/head
treehouse get --lease --no-fetch --json
```

With `--no-fetch`, Treehouse resets or creates the worktree from existing local refs and never contacts `origin`. The caller is responsible for ensuring those refs and objects are current.

`treehouse status --json` returns an array with `name`, `path`, `status`, `flavor`, `lease_id`, `lease_holder`, `leased_at`, and `processes`. `processes` is what `treehouse return` would terminate in that worktree, not every process whose working directory is inside it: the calling process and its ancestors are excluded, so running `status` from inside a pooled worktree reports what is resident in the slot instead of the shell you typed the command into. When the process table itself cannot be read, `status` is `unverified` and `processes` is empty: whether anything is running there is unknown, so the slot is not reported `available`, `dirty`, or `in-use`, and the error is printed as a warning on stderr. A lease, an owner reservation, or the slot you are standing in is still reported as such, because those facts do not depend on the scan. `flavor` is the backend the worktree's own marker identifies (`"git"` or `"jj"`) and is omitted when no marker is found. Non-leased entries use empty lease strings and a `null` timestamp. State files written before lease identities remain readable; their existing leases have an empty `lease_id` until released and acquired again.

Release a lease with `treehouse return <path>`, which terminates lingering processes and verifies that no foreign process remains before it resets the worktree, clears the lease, and returns the worktree to the pool.
If process termination or that verification fails, the command exits nonzero and leaves the worktree and lease in place instead of recycling a slot that may still be in use.
A non-interactive dirty return aborts without cleaning: prune will not reclaim that slot. Retry by pasting the printed `treehouse return --force <quoted-path>` hint (shell-quoted so copy-paste does not expand metacharacters). `--force` with no path only works from inside a repository.
When you pass an explicit path, `treehouse return` can run from outside the repository because it resolves the managed pool from that worktree path.

`treehouse return` exits 0 only when the worktree was actually returned:

| Exit | Meaning |
| ---- | ------- |
| `0`  | The worktree was returned and any lease on it was released |
| `1`  | The return failed: unmet lease conditions, process termination, or reset |
| `3`  | The worktree was not returned, and is exactly as it was found: it has uncommitted changes and cleaning was declined, or the confirmation could not be answered |

`treehouse get` uses the same exit `3` when its subshell exits and leaves the worktree dirty, because it leaks the slot the same way: the worktree stays dirty, so a later `get` skips it and `prune` will not reclaim it. Exiting a `get` subshell while another session holds a durable lease on that slot is not this case and still exits 0, because a leased slot was never that session's to return.

Exit `3` is separate from `1` because the two need different handling. A failure is worth retrying; an unreturned dirty worktree stays unreturned until someone cleans it or passes `--force`, so a caller that retries on it will loop.

For retry-safe automation, condition the return on the identity from allocation or status:

```sh
treehouse return --force \
  --if-lease-id "$lease_id" \
  --if-lease-holder "$lease_holder" \
  "$path"
```

Treehouse compares supplied conditions while holding the pool state lock. A missing lease or mismatch exits nonzero before process termination, worktree reset, or state clearing. The same lock fences a matching return through the final clear, so the identity succeeds once and cannot release a later acquisition of the same path. `--if-lease-holder` is optional; use `--if-lease-id` for ABA protection when a holder may be reused.

For backward compatibility, `treehouse return <path>` without either condition keeps its original unconditional path-only behavior. Existing path-only scripts and `treehouse get --lease` stdout are unchanged.

### Recovering missing pool state

Treehouse writes `treehouse-state.json` atomically, so a crash mid-write should leave the previous state file intact.
If an existing state file is empty, truncated, or otherwise invalid, commands do not fail just because the JSON cannot be parsed.
They print a warning and rebuild the pool entries from worktree directories still on disk.
Commands also restore an on-disk worktree that is missing from an otherwise valid state file, covering the narrow case where worktree creation succeeded but recording its quarantine failed.
Every restored entry is marked `leased` because treehouse cannot know whether it was idle, in-use, or durably leased.
Both routes scan the pool directory, so a worktree that `worktree_path` placed outside it is not rebuilt — see [Worktree path](#worktree-path) for how to remove one.

Run `treehouse status` to inspect recovered entries.
Treehouse cannot safely return these entries to the pool because recovery cannot reconstruct the trusted inventory of seeded ignored files.
State written by versions without inventory integrity data, or whose pool-local `treehouse-state.key` is missing or invalid, is handled the same way, including state rewritten after a downgrade.
After inspecting a recovered worktree, remove it by naming its exact path with `treehouse destroy <path> --include-leased --yes`.
Bulk `destroy --all` and prune leave recovered entries alone.

### Pruning stale worktrees and orphans

`treehouse prune` is a dry run by default.
By default, it lists stale idle managed worktrees that would be deleted and shows the reclaimable disk space.
Pass `treehouse prune --yes` to delete those worktrees.

By default, prune only inspects the current repository's pool and must be run inside a repository.
Pass `treehouse prune --all` or `treehouse prune --global` to inspect every managed pool under the user-level treehouse root from any directory.
Global prune reads the user-level config and hooks, derives each worktree's owning repository from version-control metadata, then fetches and checks merge safety against that repository.
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
Treehouse cannot verify orphan contents after the backing version-control metadata is gone, so each orphan candidate is marked `content could not be verified`.
Use `--verbose` to show the underlying version-control diagnostic details for skipped worktrees.

### Destroying worktrees

`treehouse destroy` is the deliberate tool for removing a worktree even though it still has unlanded work, but it is safe by default and holds itself to the same bar as `prune`.

Targets are narrow and explicit:

- `treehouse destroy <worktree-path>` targets exactly one worktree.
- `treehouse destroy <pool-path> --all` targets worktrees in THAT pool only. The pool path can be the pool directory, a worktree inside it, or the repository (`.` works from inside a repo).

There is no cross-pool or global destroy: `--all` without a pool path is an error, so a stray command can never reach beyond the pool you named.

Destroy is a dry run by default.
It prints a risk-revealing preview - one or more status labels (`[disposable]`, `[leased]`, `[in-use:<pid>]`, `[unmerged]`, `[dirty]`, `[unverified]`, or a comma-separated combination such as `[leased,dirty]`), the path, and the size of each target - and removes nothing.
Pass `--yes` to execute.
It never prints a blind "all worktrees destroyed"; the summary always reports exactly what was destroyed and what was skipped.

A bare `treehouse destroy <pool> --all --yes` removes only the genuinely disposable set (merged, clean, idle, unleased - the same set `prune` would take) and SKIPS everything else, telling you which flag would include it.
Each risky class is its own opt-in, so removing risky worktrees can never be a reflexive `--yes`:

- `--include-unlanded` also removes worktrees with uncommitted changes, a HEAD not merged into the default branch, or contents treehouse cannot verify, such as a missing backing repository (irreversible data loss).
- `--include-in-use` also removes worktrees with a running process or owner reservation; their processes are terminated cleanly first and their pids are shown in the preview.
- `--include-leased` also removes a leased worktree, but only when you name the exact worktree path. Leased worktrees are NEVER removed by `--all`; combining `--include-leased` with `--all` is rejected.

A single named worktree that is skipped for lack of a flag makes the command exit non-zero, so scripts notice that nothing happened.
Bulk `--all` skips are normal and exit zero; inspect the summary to see what remains.

#### Migrating from `--force`

The old blunt `treehouse destroy --force` flag has been removed.
It overrode every protection at once - in-use, unmerged, dirty, and leased - which is what made it dangerous.
Replace it with the specific `--include-*` flag(s) for the risk you actually intend to override, plus `--yes`:

| Old | New |
| --- | --- |
| `treehouse destroy <path> --force` | `treehouse destroy <path> --yes` (add `--include-unlanded` / `--include-in-use` / `--include-leased` as needed) |
| `treehouse destroy --all --force` | `treehouse destroy <pool> --all --yes` (add `--include-unlanded` for dirty, unmerged, or unverified targets, and `--include-in-use` for in-use targets; leased homes are never included) |

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
# Use "." to keep the pool inside the project (see "In-project storage" below).
# Use an absolute user-level root for treehouse prune --all.
# root = "$HOME/worktrees"

# Optional base branch worktrees are cut from.
# Unset infers it from the repository (see "Base branch" below).
# base_branch = "develop"

# Optional path for newly created worktrees.
# Unset uses {pool}/{slot}/{repo} (see "Worktree path" below).
# worktree_path = "{repo_parent}/{repo}-{slot}"

# Optional version-control backend. Git is the default everywhere; set "jj"
# to opt in to the experimental Jujutsu backend
# (see "Version-control backend" below).
# vcs = "jj"
```

The repo-level config takes precedence for repo-safe settings.
`treehouse prune --all` can run without a repository, so it uses only the user-level config and does not read per-repo `treehouse.toml` files while sweeping.
If no config is found, the default pool size is 16.

### Base branch

By default Treehouse infers the branch worktrees are cut from: `origin/HEAD`, then the checked-out branch, then `init.defaultBranch`.
That inference is invisible and can drift — `origin/HEAD` is only set at clone time, and some clones never have it at all.

Set it explicitly for the whole pool:

```toml
base_branch = "develop"
```

or for a single acquisition:

```sh
treehouse get --base develop
treehouse get --lease --base release/2.x --json
```

`--base` wins over `base_branch`, and both are opt-in: with neither set, the inference is unchanged.

A few things worth knowing:

- **Worktrees stay in detached HEAD.** This selects the commit a worktree starts at; it does not create or check out a branch. There is no `-b` shorthand, because `-b` means branch *creation* in git and this flag creates nothing.
- **Branch names only.** `develop`, not `origin/develop`, a tag, or a commit SHA. Whichever of `develop` and `origin/develop` is further ahead wins, preferring `origin` when they have diverged — exactly how the inferred default behaves. A tag sharing a branch's name never wins: refs are resolved fully qualified.
- **It fails closed.** A base that resolves to neither a local branch nor `origin/<branch>` is an error; Treehouse never falls back to the inferred default, which would hand you a worktree cut from the wrong branch and report success. `treehouse status` shows the resolved base, and flags a configured one it cannot resolve.
- **Returned worktrees are parked on the base they were cut from**, so the pool keeps recycling. `base_branch` wins when it is set; otherwise a slot acquired with `--base` is parked back on that branch. A slot parked elsewhere could not be reused whenever the base is not a descendant of it.
- **Existing pools migrate on their own.** A slot is recycled onto a newly requested base as long as it carries nothing beyond the base it was cut from; the two bases need no ancestry relation, so a `develop` slot rejoins a plain `treehouse get` and vice versa. A slot holding commits the new base does not contain is still refused, as always.
- **Git backend only for now.** Under the jj backend an explicit base fails with a clear error rather than silently using the default bookmark.

### Worktree path

New pool slots are created at `{pool}/{slot}/{repo}`, for example `~/.treehouse/myapp-a1b2c3/1/myapp`.
Some tooling only works when a checkout sits at a particular location relative to something else — build systems that find a project root by walking up a fixed number of directories from the package directory are the common case, and a pool three levels deeper silently resolves the wrong root.

Set `worktree_path` to place new slots yourself:

```toml
# worktrees become siblings of the repository: <parent>/myapp-1, <parent>/myapp-2
worktree_path = "{repo_parent}/{repo}-{slot}"
```

or for a single acquisition:

```sh
treehouse get --lease --worktree-path '{repo_parent}/{repo}-{slot}'
```

or for every acquisition in one shell session:

```sh
export TREEHOUSE_WORKTREE_PATH='{repo_parent}/{repo}-{slot}'
```

| Placeholder | Expands to |
|---|---|
| `{slot}` | The slot name (`1`, `2`, …). **Required** — without it every slot resolves to one directory. |
| `{repo}` | The repository directory's name |
| `{repo_parent}` | The directory holding the repository |
| `{pool}` | This repository's pool directory |

One of `{pool}` or `{repo}` is also required, because slot names are allocated per pool: a user-level `$HOME/trees/{slot}` would send the first slot of *every* repository to `$HOME/trees/1`. `{repo_parent}` does not count — two repositories side by side expand it to the same directory, so `{repo_parent}/{slot}` collides exactly the same way. It stays available as a placeholder; it just has to be paired, as in `{repo_parent}/{repo}-{slot}`.
The template must resolve to an absolute path, so anchor it on `{pool}`, `{repo_parent}`, or an absolute prefix of your own: a bare `{repo}-{slot}` is rejected rather than resolved against whichever directory you happened to run `get` from.

`$VAR` and `${VAR}` expand as well, exactly like `root`, and they expand *before* the placeholders are read — so `${HOME}/trees/{repo}-{slot}` works, and `${slot}` is an environment variable rather than the slot placeholder.
A variable the environment leaves empty is an error rather than an empty path segment, because dropping a directory would place worktrees somewhere the template does not name, and differently depending on whether the invoking shell, cron job, or CI runner exports it. `${}` and a `${` with no closing brace are errors for the same reason. A value that ends in a separator is fine — path cleaning collapses the doubled separator and the worktree lands where the template names.
The resolved value follows the same precedence as `--root` (highest first): the `--worktree-path` flag, the `TREEHOUSE_WORKTREE_PATH` environment variable, `worktree_path` in the repo-level `treehouse.toml`, then in the user-level config.

A few things worth knowing:

- **It is off by default.** With nothing set, the layout is unchanged.
- **It applies only to slots created from now on.** Worktrees already in the pool keep the path recorded in state — nothing is moved, renamed, or invalidated — so both layouts coexist in a pool until the old slots are destroyed and re-acquired. Recycling, leases, `status`, `return`, `prune`, and `destroy` all key off the recorded path, so they behave the same wherever a slot lives.
- **A template that would corrupt the pool is rejected before anything is created**: a missing `{slot}` or repository-scoping placeholder, a misspelled placeholder, a template that resolves to the same directory for every slot (path cleaning cancels `..` against the segment before it, so `{slot}/../shared` would send every slot to one directory; a `..` that only walks up inside a variable's value is fine), a path that is or contains the repository or the pool directory, and a path inside the repository working tree (where the worktree would show up as untracked content). Inside the pool directory a template must stay at `{pool}/{slot}/<name>`, because that is the depth pool-state recovery scans, and it must be spelled through the pool directory itself rather than through a symlink into it, so pool state and recovery name that worktree the same way. A path inside *another* repository's pool directory is rejected too: that pool's recovery would register the worktree as a slot of its own, which consumes one of its slots for good and leaves the owning repository unable to return it. A worktree beside the pools, under the Treehouse root, is fine — the root holds pools without being one. Placement is judged on canonicalized paths, so a symlink pointing back into the repository or the pool is caught rather than followed. Every one of these checks runs on every `get`, including the acquisitions that recycle a slot and so never use the template, so what a template is rejected for never depends on how full the pool is.
- **An existing directory is never adopted.** `{repo}` alone still collides between two repositories that share a directory name, and no template can rule every collision out, so a templated path that already exists fails the acquisition by name instead of quietly becoming a second pool's worktree. When the occupant is a worktree of this repository — which is what a lost state file leaves behind, since recovery only scans the pool directory — the error says so and points at `git worktree list` and at [Recovering missing pool state](#recovering-missing-pool-state), rather than prescribing a command whose preconditions it cannot check. Any other occupant is left alone and unnamed. The built-in layout is untouched by this check.
- **Only the worktree is deleted.** Under the built-in layout `prune` and `destroy` also remove the numbered slot directory, which exists solely to hold the worktree. A worktree placed elsewhere has a parent Treehouse does not own, so its parent is left alone.
- **Recovering a lost state file is pool-local.** `ReadState` reconstructs missing entries by scanning the pool directory, so worktrees placed outside it cannot be recovered that way; remove such a worktree with `git worktree remove` (or `jj workspace forget`) if its pool state is ever lost.

### Version-control backend (git or Jujutsu)

Treehouse works in git and [Jujutsu (jj)](https://github.com/jj-vcs/jj) repositories.
**The jj backend is experimental**: it is newer than the git backend and has seen far less production use, so treat it accordingly and report issues.
In a jj repository, pooled worktrees are [jj workspaces](https://jj-vcs.github.io/jj/latest/working-copy/#workspaces) instead of git worktrees; the pool, lease, and safety machinery is identical.

Git is the default backend everywhere, including in colocated repositories (both `.jj` and `.git`) and `.jj`-only repositories.
Opt in to the jj backend with `vcs = "jj"`, resolved in this precedence (highest first): the `TREEHOUSE_VCS` environment variable, the repo-level `treehouse.toml`, the user-level `~/.config/treehouse/config.toml`.
The jj opt-in only applies where a `.jj` directory actually exists; in a plain git repository it is silently ignored and git is used, so a shell-wide `TREEHOUSE_VCS=jj` never breaks git-only repositories.
A `vcs` value other than `"git"` or `"jj"` (e.g. `"Jujutsu"`) is ignored the same way, but warns once on stderr naming the value and where it came from, so a typo keeps commands working without silently leaving you on the wrong backend.
Pooled jj workspaces inherit the opt-in from their main repository root, so an untracked `treehouse.toml` there is enough.

The backend is resolved on every command, and existing pool slots keep the flavor they were created with: changing the opt-in does not convert worktrees already in the pool.
`destroy` and `prune` handle each slot by its own flavor (its `.git` or `.jj` marker), so a git worktree is still cleanly deregistered from git even after opting the repository into jj, and vice versa.
A slot whose marker is missing entirely (a damaged slot) is never reused, reset, or detached; `treehouse status` reports it as `damaged`, `treehouse return` only clears its lease, `treehouse lease` still protects it but reports an empty `base_branch` rather than reading one through a fallback backend, `prune` reports it as unverifiable, and `treehouse destroy <path> --include-unlanded` removes it.
`treehouse get` is flavor-aware too: it only reuses slots matching the backend the repository currently selects, and creates new slots with that backend, so a caller who opted in to jj is never handed a git worktree (or vice versa).
Old-flavor slots stay in the pool untouched — `treehouse status` marks them and they count toward `max_trees` — until you migrate them: `treehouse destroy` the old slots and re-acquire with `treehouse get`.

jj-backend notes:

- Pooled worktrees are jj workspaces and are not colocated: they contain `.jj` but no `.git`, so run jj commands (not git) inside them.
- A worktree is considered dirty when its working-copy commit `@` is non-empty or has a description.
- Resets abandon only the working-copy commit and are recoverable with `jj op restore`.
- Merge detection uses ancestry; squash-merged work is treated as unmerged, so lifecycle commands err on the side of keeping it.
- The default branch resolves to the `main`/`master`/`trunk` bookmark, preferring origin.
- A pooled jj workspace whose backing repository was deleted is classified as an orphan just like a git worktree: `prune` reports it, and `prune --prune-orphans --yes` reclaims it.

### Worktree root

The worktree root can also be set without a config file, and the resolved value follows this precedence (highest first):

1. The `--root` flag (e.g. `treehouse get --root .`)
2. The `TREEHOUSE_ROOT` environment variable
3. `root` in the repo-level `treehouse.toml`
4. `root` in the user-level `~/.config/treehouse/config.toml`
5. The default, `~/.treehouse`

A relative value (including `.`) is resolved from the repo root, exactly like a relative `root` in config; `treehouse` is always appended, so `--root .` places the pool at `<repo>/.treehouse/`.

### In-project storage

By default the pool lives in the global `~/.treehouse` store. Set the root to `.` to keep it **inside the project** instead:

```sh
treehouse get --root .          # one-off
export TREEHOUSE_ROOT=.         # for a shell session
```

or commit it for the whole repo in `treehouse.toml`:

```toml
root = "."
```

This is **opt-in**; the default global store is unchanged. In-project mode:

- Places the pool at `<repo>/.treehouse/`, so worktrees sit next to the code and are **removed with the project** (`rm -rf <repo>` leaves no global orphan).
- Git-ignores the pool directory automatically, so it stays out of `git add`.
- Is not reached by `treehouse prune --all`, which only sweeps the global root; in-project pools are removed with the project instead.

### Hooks

You can run commands automatically at worktree lifecycle points by adding a `[hooks]` section to the user-level config at `~/.config/treehouse/config.toml`.
Hooks in repo-level `treehouse.toml` are ignored for safety.
`treehouse destroy` always reads `pre_destroy` from the user-level config because it can target a pool by path.

```toml
[hooks]
post_create = ["./scripts/setup-venv.sh"]
pre_destroy = ["./scripts/teardown.sh"]
```

- `post_create` runs after a worktree is provisioned or reset and right before `treehouse get` hands it to you.
  For `treehouse get --lease`, stdout from `post_create` is routed to stderr so stdout remains the leased path.
- `pre_destroy` runs before a worktree is removed by `treehouse destroy <path> --yes`, `treehouse destroy <pool> --all --yes`, or prune deletion commands such as `treehouse prune --yes` and `treehouse prune --prune-orphans --yes`.

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
