# AGENTS.md governance addition - verification

Target commit: `1009f1d152481b16d6b3d85d95fe61542ed9e5a9`.

The change from `c76015d914e8ae7068c024c68c58860e10e8ee52` modifies only `AGENTS.md` and adds seven lines.

The end-user-visible document tail is:

```md
## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
```

Verified with:

```sh
git diff --check c76015d914e8ae7068c024c68c58860e10e8ee52 1009f1d152481b16d6b3d85d95fe61542ed9e5a9
test "$(git diff --name-only c76015d914e8ae7068c024c68c58860e10e8ee52 1009f1d152481b16d6b3d85d95fe61542ed9e5a9)" = "AGENTS.md"
test "$(git diff --numstat c76015d914e8ae7068c024c68c58860e10e8ee52 1009f1d152481b16d6b3d85d95fe61542ed9e5a9)" = "7\t0\tAGENTS.md"
diff -u <(printf '%s\\n' '## Maintaining this file' '' 'Keep this file for knowledge useful to almost every future agent session in this project.' 'Do not repeat what the codebase already shows; point to the authoritative file or command instead.' 'Prefer rewriting or pruning existing entries over appending new ones.' 'When updating this file, preserve this bar for all agents and keep entries concise.') <(git show HEAD:AGENTS.md | tail -n 6)
```
