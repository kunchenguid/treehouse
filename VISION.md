# Vision

`treehouse` exists to make parallel coding-agent work routine by giving each task a fast, isolated, reusable Git worktree without making people or orchestration systems manage worktrees themselves.
It serves interactive users and automated callers, and owns the worktree lifecycle rather than agent orchestration or the user's development workflow.

## Isolation and reuse

Each acquisition must receive a worktree that no other acquisition can claim until it is returned.
Dirty, in-use, leased, changing, or unverifiable worktrees must not be silently recycled.
An explicit guest operation may attach to an existing worktree, but it must not change ownership or lifecycle state.

Pooling makes isolation fast, but a reused worktree must meet the same correctness bar as a new one.
Treehouse isolates working directories and lifecycle ownership; it is not a security sandbox.

## Safe lifecycle operations

Treehouse should delete or reset user work only when the scope is clear and the required facts can be verified against current Git, process, and lifecycle state.
Under uncertainty, leave the worktree in place with an actionable explanation, and verify the safety facts again at deletion time.

Destructive commands should preview their effect by default, require explicit intent to act, and gate independent risks separately.
Blanket force options, ambiguous targets, and global delete-everything paths should be resisted.
Safe broad cleanup may inspect many pools, but leases and unverified work must remain protected.

Backward compatibility matters, but it does not justify preserving an interface that can erase work too easily.
When safety requires a breaking change, make the risk and migration path plain.

## Ownership and interface

A live process, a short lifecycle reservation, and a durable lease represent different facts and should not be inferred from one another.
Unknown ownership after a crash or state failure should be quarantined until a person explicitly releases it.
Recovery should preserve work first, then restore reuse.

Users and callers should be able to see which worktree they have, whether it is available, why it was skipped, and what action is safe next without reading internal state.
Human-facing interfaces that operate on a worktree should use the 🌳 tree emoji as consistent Treehouse branding and a small bit of delight.
Machine-facing commands should keep captured data separate from diagnostics and use exit status honestly.

## Scope and evaluation

Treehouse should remain a focused local CLI primitive that composes with Git, shells, coding agents, terminal tools, and automation.
Integrations are welcome when they preserve the same lifecycle guarantees, remain optional, and degrade cleanly when the other tool is absent.
No particular agent, model, shell, multiplexer, or hosted service should be required.

Repository configuration must not cause repository-controlled code to execute automatically.
User-approved executable customization belongs at the user trust boundary, and core behavior should remain portable across supported operating systems.

A change aligns when it measurably reduces the time or coordination needed to obtain an isolated environment, makes ownership and cleanup easier to understand, or strengthens recovery without weakening these guarantees.
Core lifecycle, concurrency, and destructive changes need realistic end-to-end and failure validation.
Changes should be resisted when they turn Treehouse into a general Git workflow manager, absorb agent-orchestration policy, make a daemon or hosted service necessary, trade safety for convenience, or expand scope without improving isolated parallel work.
