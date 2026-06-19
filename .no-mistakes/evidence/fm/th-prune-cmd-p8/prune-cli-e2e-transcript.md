# treehouse prune CLI evidence

Temporary fixture root: `/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid`

This transcript exercises the real CLI from a disposable git repo with a bare `origin` and isolated fake home.

## Setup

$ go build -o /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/treehouse .

$ git init --bare --initial-branch=main /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/remote.git
Initialized empty Git repository in /private/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/remote.git/

$ git init --initial-branch=main /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo
Initialized empty Git repository in /private/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo/.git/
[main (root-commit) 59b1b76] initial commit
 1 file changed, 1 insertion(+)
 create mode 100644 README.md
To /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/remote.git
 * [new branch]      main -> main
branch 'main' set up to track 'origin/main'.

$ cat > /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.config/treehouse/config.toml
[hooks]
pre_destroy = ["pwd > /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/pre-destroy-hook-pwd.txt"]

## Dry-run lists stale idle worktree without deleting it

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home SHELL=true treehouse get
🌳 Setting up worktree...
🌳 Entered worktree at ~/.treehouse/repo-87406e/1/repo. Type 'exit' to return.
🌳 Worktree returned to pool.

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home treehouse prune
🌳 Dry run: would prune 1 stale worktree and reclaim 335 B.
1     335 B  ~/.treehouse/repo-87406e/1/repo
🌳 Re-run with --yes to delete these worktrees.

Filesystem check: dry-run preserved `/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo`.

## --yes deletes the stale idle worktree and runs pre_destroy hook

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home treehouse prune --yes
🌳 Pruned 1 stale worktree and freed 335 B.
1     335 B  ~/.treehouse/repo-87406e/1/repo

$ cat /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/pre-destroy-hook-pwd.txt
/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo

Filesystem check: --yes removed `/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo`, and pre_destroy ran in that worktree before deletion.

## Dirty idle worktree is skipped and preserved

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home SHELL=true treehouse get
🌳 Setting up worktree...
🌳 Entered worktree at ~/.treehouse/repo-87406e/1/repo. Type 'exit' to return.
🌳 Worktree returned to pool.

$ printf local-edit > /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo/uncommitted.txt

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home treehouse prune --yes
🌳 No stale worktrees pruned.
🌳 Skipped 1 unsafe idle worktree:
1     uncommitted changes  ~/.treehouse/repo-87406e/1/repo

Filesystem check: dirty worktree remained at `/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo`, and pre_destroy did not run for the skipped worktree.

## Clean worktree with unmerged commit is skipped and preserved

$ git -C /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo clean -fd
Removing uncommitted.txt

$ git -C /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo switch -c evidence-unmerged
Switched to a new branch 'evidence-unmerged'

$ git -C /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo commit -am "unmerged evidence"
[evidence-unmerged 2cfa7ec] unmerged evidence
 1 file changed, 1 insertion(+), 1 deletion(-)

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home treehouse prune --yes
🌳 No stale worktrees pruned.
🌳 Skipped 1 unsafe idle worktree:
1     HEAD is not merged into refs/remotes/origin/main  ~/.treehouse/repo-87406e/1/repo

Filesystem check: unmerged worktree remained at `/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/1/repo`, and pre_destroy did not run for the skipped worktree.

## In-use worktree is ignored by prune and preserved

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home SHELL=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/active-shell.sh treehouse get &
🌳 Setting up worktree...
🌳 Entered worktree at ~/.treehouse/repo-87406e/2/repo. Type 'exit' to return.

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home treehouse status
1     dirty        ~/.treehouse/repo-87406e/1/repo
2     in-use       ~/.treehouse/repo-87406e/2/repo
                   sh (71992)

$ cd /var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/repo && HOME=/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home treehouse prune --yes
🌳 No stale worktrees pruned.
🌳 Skipped 1 unsafe idle worktree:
1     uncommitted changes  ~/.treehouse/repo-87406e/1/repo

Filesystem check: active worktree remained at `/var/folders/5x/4nqprlbx0518k3ybcb1sz6gr0000gn/T/tmp.rPtQnkbTid/home/.treehouse/repo-87406e/2/repo` while prune ran, and pre_destroy did not run.

$ wait for active treehouse get to exit
🌳 Setting up worktree...
🌳 Entered worktree at ~/.treehouse/repo-87406e/2/repo. Type 'exit' to return.
🌳 Worktree returned to pool.

Evidence result: PASS - dry-run listed reclaimable stale worktrees, --yes deleted only the stale idle worktree, pre_destroy ran only for that deletion, and dirty, unmerged, and in-use worktrees were preserved.
