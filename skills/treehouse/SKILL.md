---
name: treehouse
description: "Get an isolated, pre-warmed git worktree from a managed pool so multiple agents can work on the same repo in parallel without cloning or stepping on each other. Use when you need a clean worktree, want to run work in isolation, are coordinating parallel agents, or are running inside herdr and need a worktree for a new pane. Commands: treehouse get / get --lease / status / return / prune / destroy."
---

# treehouse - agent skill

treehouse manages a pool of reusable, pre-warmed git worktrees per repository.
Each worktree is an isolated checkout that keeps its dependencies and build cache between uses, so an agent gets a clean environment instantly instead of cloning the repo or fighting another agent over the working tree.

Worktrees use detached HEAD reset to the latest default branch, so there are no branch-name conflicts.
There is no daemon: every operation is an inline CLI command.

## when to use this

- you need a clean, isolated place to make changes without disturbing the user's working tree
- you are one of several agents working on the same repo and must not collide
- you are running inside herdr and want a fresh worktree for a new pane or sibling agent (see "running under herdr" below)

## get a worktree

Run treehouse from inside the repository:

```bash
treehouse get
```

This acquires a worktree from the pool (or creates one), then opens a subshell inside it.
The worktree path is also exported as `$TREEHOUSE_DIR`.
Do your work in that subshell, then `exit` to return the worktree to the pool.
On exit treehouse detaches HEAD, terminates lingering processes, resets the worktree, and marks it available again.

The banner printed on entry looks like:

```
🌳 Entered worktree at ~/.treehouse/myproject-a1b2c3/1/myproject. Type 'exit' to return.
```

## lease a worktree without a subshell

When you need a worktree to persist as a durable home with no long-lived process inside it (for example, a scripted caller that captures the path), lease it:

```bash
path=$(treehouse get --lease)
```

`--lease` prints only the worktree's absolute path to stdout; every human-facing banner goes to stderr, so command substitution stays clean.
A leased worktree is never handed out by a later `get` and never removed by `prune`, even with zero processes inside it, until you release it.
Record who holds it with `--lease-holder <label>` (or `$TREEHOUSE_LEASE_HOLDER`).

Release a lease (from anywhere, by naming the path):

```bash
treehouse return /abs/path/to/worktree
```

`return` clears the lease, terminates lingering processes, resets the worktree, and returns it to the pool.

## inspect the pool

```bash
treehouse status
```

Each worktree is shown as `available`, `in-use`, `dirty`, `leased` (with its holder), or `you're here`.

## clean up

`prune` and `destroy` are both dry runs by default and only act when you pass `--yes`.

```bash
treehouse prune            # list stale, idle, merged, clean worktrees that could be removed
treehouse prune --yes      # actually remove them
treehouse prune --all      # sweep every managed pool under the treehouse root
```

`destroy` is the deliberate tool for removing a specific worktree or a whole pool, and it is safe by default.
Each risky class is its own explicit opt-in (`--include-unlanded`, `--include-in-use`, `--include-leased`); a bare `destroy <pool> --all --yes` only removes the genuinely disposable set.

```bash
treehouse destroy /abs/path/to/worktree           # dry-run preview of one worktree
treehouse destroy /abs/path/to/worktree --yes      # remove it
```

## running under herdr

herdr (https://herdr.dev) is the terminal multiplexer this skill composes with.
treehouse gives an agent an isolated worktree; herdr gives it a pane, sibling agents, and coordination.
They are complementary: treehouse handles worktree isolation, herdr handles the panes and agents that work in them.

When treehouse runs inside herdr (`HERDR_ENV=1`) and the herdr CLI is present, `treehouse get` opens the worktree in its own herdr pane (leased, held by `herdr`) instead of nesting a subshell in your current pane, and it exports `TREEHOUSE_HERDR=1` into that pane.
The worktree still returns to the pool when you exit the pane.
Pass `--no-herdr` (or set `TREEHOUSE_NO_HERDR=1`) to force the classic in-place subshell.

If you are an agent that just landed in a treehouse worktree and you see `HERDR_ENV=1` (or `TREEHOUSE_HERDR=1`), use the **`/herdr` skill** to drive the runtime around you: split panes, start servers and watch logs in adjacent panes, spawn sibling agents, and coordinate with `herdr wait agent-status`.

A common parallel-agent loop, combining both skills:

```bash
# 1. lease a fresh worktree for a sibling task (path on stdout, banners on stderr)
wt=$(treehouse get --lease --lease-holder sibling-feature-x)

# 2. use the /herdr skill to open a pane there and start an agent in it
#    (see the herdr skill for the exact pane/agent commands)
herdr agent start feature-x --cwd "$wt" --split right -- claude

# 3. when the sibling is done, release the worktree back to the pool
treehouse return "$wt"
```

For everything about panes, tabs, workspaces, waiting on output, and coordinating agents, defer to the `/herdr` skill.

## installing this skill

Copy this directory to your user-level skills directory so the agent can discover it:

```bash
cp -r skills/treehouse ~/.claude/skills/treehouse
# or, from a treehouse checkout:
make install-skill
```
