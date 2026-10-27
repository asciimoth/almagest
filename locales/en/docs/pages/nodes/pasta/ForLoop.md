Runs a counted sequence of integer indexes. Connect [Loop](@port/right/Loop) to
one [Iter](@docs/node/pasta/Iter) node, and make each [Body](@port/right/Body)
path finish at that [Iter](@docs/node/pasta/Iter) node's **Continue** or
**Break** connector. A trigger does nothing when [Loop](@port/right/Loop) is not
connected to an [Iter](@docs/node/pasta/Iter) node. Start the loop with the
node's trigger action or through [Trigger](@port/left/Trigger). The
[Trigger](@port/left/Trigger) input accepts multiple trigger sources.

## Range and step

[End index](@port/left/End%20index) is exclusive. For a positive
[Step](@port/left/Step), the loop runs while the current index is less than
[End index](@port/left/End%20index). For a negative [Step](@port/left/Step), it
runs while the current index is greater than
[End index](@port/left/End%20index).

For example:

- [Start](@port/left/Start%20index) `0`, [End](@port/left/End%20index) `5`,
  [Step](@port/left/Step) `2` produces `0`, `2`, `4`.
- [Start](@port/left/Start%20index) `5`, [End](@port/left/End%20index) `0`,
  [Step](@port/left/Step) `-2` produces `5`, `3`, `1`.

A zero [Step](@port/left/Step) produces no iterations. A positive
[Step](@port/left/Step) also produces no iterations when
[Start index](@port/left/Start%20index) is equal to or greater than
[End index](@port/left/End%20index). A negative [Step](@port/left/Step) produces
no iterations when [Start index](@port/left/Start%20index) is equal to or less
than [End index](@port/left/End%20index). In all these cases, the node sends
[Completed](@port/right/Completed) immediately.

A disconnected [Start index](@port/left/Start%20index) or
[End index](@port/left/End%20index) input has the value `0`. A disconnected
[Step](@port/left/Step) input has the value `1`. Each of these inputs accepts
one connection.

## Iteration order

When triggered, ForLoop takes a snapshot of
[Start index](@port/left/Start%20index), [End index](@port/left/End%20index),
and [Step](@port/left/Step). Changes to these inputs do not change the active
run. For each iteration, the node does these actions in order:

1. It starts the matching iteration on the connected
   [Iter](@docs/node/pasta/Iter) node.
1. It sends the current [Index](@port/right/Index).
1. It sends [Body](@port/right/Body).
1. It waits for [Iter](@docs/node/pasta/Iter) to receive **Continue** or
   **Break**.

**Continue** adds [Step](@port/left/Step) to the current index. The loop starts
the next iteration if the new index is still in range. Otherwise, it sends
[Completed](@port/right/Completed) and returns to the `waiting` state. **Break**
sends [Completed](@port/right/Completed) immediately without another iteration.
If the [Body](@port/right/Body) path sends neither **Continue** nor **Break**,
the loop stays in the `looping` state and does not make progress.

## Additional triggers and graph changes

A trigger received during an active run is queued. Each queued run keeps the
[Start index](@port/left/Start%20index), [End index](@port/left/End%20index),
and [Step](@port/left/Step) values that were current when that trigger arrived.
Queued runs start in arrival order after the preceding run sends
[Completed](@port/right/Completed).

Adding or removing any connection on ForLoop cancels the active run and all
queued runs. This cancellation does not send [Completed](@port/right/Completed).
After the connections are stable, send a new trigger to start again.

## Graph example

This loop sends one body trigger for each index from 0 through 2. The direct
Continue path ends each iteration. When the range is complete, ForLoop triggers
the popup. Replace the direct body path with the work that must run on each
iteration.

```graph
{
  "Start loop": {
    "Class": "pasta/Trigger",
    "Pos": "{\"x\":31,\"y\":80}",
    "Links": ["Trigger -> [Count indexes] Trigger"],
  },
  "End index": {
    "Class": "pasta/IntConstant",
    "Pos": "{\"x\":31,\"y\":187}",
    "Label": "3",
    "Links": ["output -> [Count indexes] End index"],
    "value": 3,
  },
  "Count indexes": {
    "Class": "pasta/ForLoop",
    "Pos": "{\"x\":261,\"y\":120}",
    "Links": [
      "Loop -> [End iteration] Loop",
      "Body -> [End iteration] Continue",
      "Completed -> [Done popup] Trigger",
    ],
  },
  "End iteration": {
    "Class": "pasta/Iter",
    "Pos": "{\"x\":509,\"y\":80}",
  },
  "Done popup": {
    "Class": "pasta/PopUp",
    "Pos": "{\"x\":509,\"y\":187}",
  },
}
```
