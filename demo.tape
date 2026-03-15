Output demo.gif

Require treehouse
Require git

Set Shell "bash"
Set FontSize 20
Set Width 900
Set Height 600
Set Padding 20
Set Theme "Catppuccin Mocha"

# Suppress update check
Env TREEHOUSE_NO_UPDATE_CHECK "1"
Env SHELL "/bin/bash"

# Setup: create a git repo with a local bare remote, off-screen
Hide
Type "TMPDIR=$(mktemp -d) && git init --bare $TMPDIR/remote.git && git init $TMPDIR/myproject && cd $TMPDIR/myproject && git remote add origin $TMPDIR/remote.git && git commit --allow-empty -m 'init' && git push origin main && clear && cd $TMPDIR"
Enter
Sleep 2s
Show

Type "cd myproject"
Enter
Sleep 1s

# Demo: treehouse
Type "# ready to start a new task? "
Sleep 1s
Type "just run treehouse"
Sleep 1s
Ctrl+U
Sleep 1s
Type "treehouse"
Enter
Sleep 2s

# Check status
Type "treehouse status"
Enter
Sleep 2s

# Explain: run agent
Type "# run your agent and get some work done..."
Sleep 3s
Ctrl+U

# Explain: parallel session
Type "# need to do something in parallel? "
Sleep 1s
Type "just run treehouse"
Sleep 1s
Ctrl+U
Sleep 1s
Type "treehouse"
Enter
Sleep 2s

# Check status
Type "treehouse status"
Enter
Sleep 2s

# Explain: run agent
Type "# when you are done, just exit the subshell"
Sleep 3s
Ctrl+U

# Exit subshell (returns worktree to pool)
Type "exit"
Enter
Sleep 2s

# Check status
Type "treehouse status"
Enter
Sleep 2s

# Explain: treehouse
Type "# manage worktrees without managing worktrees. "
Sleep 1s
Type "treehouse "
Sleep 1s
Type "🌳"
Sleep 10s

