Ends each iteration of one [ForLoop](@docs/node/pasta/ForLoop) or
[WhileLoop](@docs/node/pasta/WhileLoop). Connect the loop node's
[Loop](@port/left/Loop) connector to [Loop](@port/left/Loop) on Iter. This
connection associates Iter with that loop and accepts only one loop node.

Connect the end of each **Body** path to [Continue](@port/left/Continue) or
[Break](@port/left/Break):

- [Continue](@port/left/Continue) ends the current iteration and asks the loop
  to advance. A [ForLoop](@docs/node/pasta/ForLoop) starts the next in-range
  index or completes when its range has ended. A
  [WhileLoop](@docs/node/pasta/WhileLoop) starts its next iteration.
- [Break](@port/left/Break) ends the current iteration and finishes the loop
  without another iteration.

The loop opens an iteration on Iter before it sends its **Index** value and
**Body** trigger. It then waits until Iter receives
[Continue](@port/left/Continue) or [Break](@port/left/Break). If neither input
receives a trigger, the loop stays in its active state and does not make
progress.

For one active iteration, the first trigger received by
[Continue](@port/left/Continue) or [Break](@port/left/Break) wins. Iter sends
one result to the associated loop and closes that iteration. Further triggers
are ignored until the loop opens the next iteration. Each trigger input can
accept multiple trigger sources. Trigger requests are not treated as
[Continue](@port/left/Continue) or [Break](@port/left/Break) events.

[Continue](@port/left/Continue) and [Break](@port/left/Break) have no effect
when [Loop](@port/left/Loop) is disconnected or when the associated loop has no
active iteration. Disconnecting or replacing the [Loop](@port/left/Loop)
connection also clears the current iteration. Triggering Iter itself has no
effect.
