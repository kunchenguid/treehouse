# treehouse global prune CLI evidence

This transcript uses a fake HOME under `.no-mistakes/tmp`, four real git repositories, and one non-managed directory under the treehouse root.
Commands marked outside were run from `./.no-mistakes/tmp/global-prune-e2e/outside` with `GIT_CEILING_DIRECTORIES` set to the project root, so Git treats that directory as outside any repository while all writes stay inside the worktree.

## Managed Worktrees Created

- safe candidate: `~/.treehouse/safe-repo-7af752/1/safe-repo`
- dirty skip: `~/.treehouse/dirty-repo-f6c3a9/1/dirty-repo`
- unmerged skip: `~/.treehouse/unmerged-repo-1cd186/1/unmerged-repo`
- in-use/reserved skip: `~/.treehouse/inuse-repo-16607a/1/inuse-repo`
- non-managed directory: `~/.treehouse/not-managed-pool/1/not-managed`

## Help Shows Opt-In Global Flags

```console
$ treehouse prune --help
Remove stale idle worktrees from the pool to reclaim disk space.

A worktree is stale only when treehouse manages it, no owner reservation or
running process is using it, it has no uncommitted changes, and its HEAD is
already merged into the default branch.

Prune is a dry run by default. Pass --yes to delete the listed worktrees.
Pass --all to sweep every pool under the treehouse root from any directory.

Usage:
  treehouse prune [flags]

Flags:
      --all      Prune stale worktrees across every pool under the treehouse root
      --global   Alias for --all
  -h, --help     help for prune
      --yes      Delete stale worktrees instead of doing a dry run
[exit 0]
```

## No Flag Still Requires A Git Repository

```console
$ cd ./.no-mistakes/tmp/global-prune-e2e/outside
$ treehouse prune
not in a git repository: git rev-parse --show-toplevel: fatal: not a git repository (or any of the parent directories): .git
[exit 1]
```

## Global Dry Run Works From Outside A Repo

```console
$ cd ./.no-mistakes/tmp/global-prune-e2e/outside
$ treehouse prune --all
🌳 Dry run: would prune 1 stale worktree across 4 pools and reclaim 401 B.
1     401 B  ~/.treehouse/safe-repo-7af752/1/safe-repo
🌳 Skipped 2 unsafe idle worktrees:
1     uncommitted changes                               ~/.treehouse/dirty-repo-f6c3a9/1/dirty-repo
1     HEAD is not merged into refs/remotes/origin/main  ~/.treehouse/unmerged-repo-1cd186/1/unmerged-repo
🌳 Re-run with --all --yes to delete these worktrees.
[exit 0]
```

## --global Alias Matches --all

```console
$ cd ./.no-mistakes/tmp/global-prune-e2e/outside
$ treehouse prune --global
🌳 Dry run: would prune 1 stale worktree across 4 pools and reclaim 401 B.
1     401 B  ~/.treehouse/safe-repo-7af752/1/safe-repo
🌳 Skipped 2 unsafe idle worktrees:
1     uncommitted changes                               ~/.treehouse/dirty-repo-f6c3a9/1/dirty-repo
1     HEAD is not merged into refs/remotes/origin/main  ~/.treehouse/unmerged-repo-1cd186/1/unmerged-repo
🌳 Re-run with --all --yes to delete these worktrees.
[exit 0]
```

## Repo-Scoped Prune Does Not Sweep Other Pools

```console
$ cd ./.no-mistakes/tmp/global-prune-e2e/repos/dirty-repo
$ treehouse prune --yes
🌳 No stale worktrees pruned.
🌳 Skipped 1 unsafe idle worktree:
1     uncommitted changes  ~/.treehouse/dirty-repo-f6c3a9/1/dirty-repo
[exit 0]
```

safe_worktree_after_repo_scoped_dirty_prune=exists

## In-Use Worktree Is Visible To Users

```console
$ cd ./.no-mistakes/tmp/global-prune-e2e/repos/inuse-repo
$ treehouse status
1     in-use       ~/.treehouse/inuse-repo-16607a/1/inuse-repo
                   bash (42402), sleep (42422)
[exit 0]
```

## Global --yes Deletes Only Safe Candidates

```console
$ cd ./.no-mistakes/tmp/global-prune-e2e/outside
$ treehouse prune --all --yes
🌳 Pruned 1 stale worktree across 4 pools and freed 401 B.
1     401 B  ~/.treehouse/safe-repo-7af752/1/safe-repo
🌳 Skipped 2 unsafe idle worktrees:
1     uncommitted changes                               ~/.treehouse/dirty-repo-f6c3a9/1/dirty-repo
1     HEAD is not merged into refs/remotes/origin/main  ~/.treehouse/unmerged-repo-1cd186/1/unmerged-repo
[exit 0]
```

## Post-Prune State

```text
safe_after_global_yes=missing
dirty_after_global_yes=exists
unmerged_after_global_yes=exists
inuse_after_global_yes=exists
nonmanaged_after_global_yes=exists
pre_destroy_hook_log:
~/.treehouse/safe-repo-7af752/1/safe-repo
```
