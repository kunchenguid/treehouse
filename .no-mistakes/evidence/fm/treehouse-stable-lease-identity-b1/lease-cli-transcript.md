# Stable lease identity CLI validation

This transcript came from the compiled `treehouse` binary in a fresh local Git repository with an isolated `HOME`. It exercises the end-user JSON and conditional-return surfaces.

## 1. Allocate a lease as `automation-A`

```json
{"path":"/tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo","lease_id":"4d6e581c9889f539cf3defb32209c991","lease_holder":"automation-A","leased_at":"2026-07-20T12:20:27.465416-07:00"}
```

## 2. Status exposes the same identity

```json
[{"name":"1","path":"/tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo","status":"leased","lease_id":"4d6e581c9889f539cf3defb32209c991","lease_holder":"automation-A","leased_at":"2026-07-20T12:20:27.465416-07:00","processes":[]}]
```

## 3. A wrong identity is rejected without releasing the lease

Command exited 1:

```text
failed to return worktree: lease precondition failed: lease identity does not match worktree /tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo
```

Status still reports the original lease:

```json
[{"name":"1","path":"/tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo","status":"leased","lease_id":"4d6e581c9889f539cf3defb32209c991","lease_holder":"automation-A","leased_at":"2026-07-20T12:20:27.465416-07:00","processes":[]}]
```

## 4. The correct identity releases successfully

```text
🌳 Worktree returned to pool.
```

## 5. Same path and holder get a new identity

Old path:

```text
/tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo
```

New allocation:

```json
{"path":"/tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo","lease_id":"42e716b76d721ab9212dba38c062f450","lease_holder":"automation-A","leased_at":"2026-07-20T12:20:27.800304-07:00"}
```

Identity comparison: `ids_differ=true`.

## 6. The old identity cannot release the newer same-holder lease

Command exited 1:

```text
failed to return worktree: lease precondition failed: lease identity does not match worktree /tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo
```

Status confirms that the newer lease remains current after the stale ABA attempt:

```json
[{"name":"1","path":"/tmp/treehouse-lease-validation.IP9UTX/home/.treehouse/repo-f547e7/1/repo","status":"leased","lease_id":"42e716b76d721ab9212dba38c062f450","lease_holder":"automation-A","leased_at":"2026-07-20T12:20:27.800304-07:00","processes":[]}]
```
