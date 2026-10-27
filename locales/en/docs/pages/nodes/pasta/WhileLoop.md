Runs a trigger flow repeatedly until it receives a Break result. Connect
[Loop](@port/right/Loop) to one [Iter](@docs/node/pasta/Iter) node. This
connection associates the [Iter](@docs/node/pasta/Iter) node with this WhileLoop
and accepts only one Iter node.

Start the loop with the node's manual trigger action or through
[Trigger](@port/left/Trigger). The [Trigger](@port/left/Trigger) input accepts
multiple trigger sources. A trigger does nothing when [Loop](@port/right/Loop)
is not connected to an [Iter](@docs/node/pasta/Iter) node.

## Iteration order

For each iteration, WhileLoop does these actions in order:

1. It opens a new iteration on the associated [Iter](@docs/node/pasta/Iter)
   node.
1. It sends [Body](@port/right/Body) to all connected [Body](@port/right/Body)
   paths.
1. It waits for the [Iter](@docs/node/pasta/Iter) node to receive **Continue**
   or **Break**.

**Continue** ends the current iteration and immediately starts the next one.
**Break** ends the current iteration and returns the node to its `waiting`
state. The node then sends [Completed](@port/right/Completed) to all connected
consumers. The node shows `looping` while it waits for **Continue** or
**Break**.

Connect the end of each Body path to **Continue** or **Break** on the associated
[Iter](@docs/node/pasta/Iter) node. For each iteration, make sure that only one
of these inputs receives a trigger. If neither receives a trigger, the loop
stays active and does not make progress.

## Queued runs and cancellation

A trigger received during an active run is queued. Each trigger adds one run.
Queued runs start in arrival order after the active run sends
[Completed](@port/right/Completed). Each queued run also continues until its own
**Break** result.

Adding or removing any connection on WhileLoop cancels the active run and all
queued runs. This cancellation does not send [Completed](@port/right/Completed).
Restored and pasted nodes also start in the `waiting` state with no queued runs.

WhileLoop has no built-in condition, delay, or iteration limit. An unconditional
**Continue** path can run without end and keep the workspace busy. Add a
reachable **Break** path, and avoid starting a run unless its
[Body](@port/right/Body) path can always reach the associated
[Iter](@docs/node/pasta/Iter) node.

## Graph example

SelectOut sends each body trigger to Continue or Break. In this safe example,
the `false` constant selects Break and the loop runs once. Replace the constant
with a condition that stays `true` while another iteration is required and
becomes `false` to select Break.

```graph
{
  "Start loop": {
    "Class": "pasta/Trigger",
    "Pos": "{\"x\":31,\"y\":53}",
    "Links": ["Trigger -> [Repeat work] Trigger"],
  },
  "Stop condition": {
    "Class": "pasta/BoolConstant",
    "Pos": "{\"x\":31,\"y\":187}",
    "Label": "false",
    "Links": ["output -> [Select result] Selector"],
    "value": false,
  },
  "Repeat work": {
    "Class": "pasta/WhileLoop",
    "Pos": "{\"x\":245,\"y\":80}",
    "Links": [
      "Loop -> [End iteration] Loop",
      "Body -> [Select result] In",
    ],
  },
  "Select result": {
    "Class": "pasta/SelectOut",
    "Primary": "pasta/trigger",
    "Pos": "{\"x\":476,\"y\":120}",
    "Links": [
      "Out 0 -> [End iteration] Break",
      "Out 1 -> [End iteration] Continue",
    ],
  },
  "End iteration": {
    "Class": "pasta/Iter",
    "Pos": "{\"x\":706,\"y\":120}",
  },
}
```
