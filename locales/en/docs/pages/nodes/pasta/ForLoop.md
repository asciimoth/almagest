Runs a counted sequence of integer indexes. Connect **Loop** to one [Iter](@docs/node/pasta/Iter) node,
and make each **Body** path finish at that [Iter](@docs/node/pasta/Iter) node's **Continue** or **Break**
connector. A trigger does nothing when Loop is not connected to an [Iter](@docs/node/pasta/Iter) node.
Start the loop with the node's trigger action or through **Trigger**. The
Trigger input accepts multiple trigger sources.

## Range and step

**End index** is exclusive. For a positive **Step**, the loop runs while the
current index is less than End index. For a negative Step, it runs while the
current index is greater than End index.

For example:

- Start `0`, End `5`, Step `2` produces `0`, `2`, `4`.
- Start `5`, End `0`, Step `-2` produces `5`, `3`, `1`.

A zero Step produces no iterations. A positive Step also produces no
iterations when Start is equal to or greater than End. A negative Step
produces no iterations when Start is equal to or less than End. In all these
cases, the node sends **Completed** immediately.

A disconnected Start or End input has the value `0`. A disconnected Step
input has the value `1`. Each of these inputs accepts one connection.

## Iteration order

When triggered, ForLoop takes a snapshot of Start index, End index, and Step.
Changes to these inputs do not change the active run. For each iteration, the
node does these actions in order:

1. It starts the matching iteration on the connected [Iter](@docs/node/pasta/Iter) node.
2. It sends the current **Index**.
3. It sends **Body**.
4. It waits for [Iter](@docs/node/pasta/Iter) to receive **Continue** or **Break**.

**Continue** adds **Step** to the current index. The loop starts the next iteration if
the new index is still in range. Otherwise, it sends **Completed** and returns to
the `waiting` state. **Break** sends **Completed** immediately without another
iteration. If the **Body** path sends neither **Continue** nor **Break**, the loop stays
in the `looping` state and does not make progress.

## Additional triggers and graph changes

A trigger received during an active run is queued. Each queued run keeps the
Start, End, and Step values that were current when that trigger arrived.
Queued runs start in arrival order after the preceding run sends Completed.

Adding or removing any connection on ForLoop cancels the active run and all
queued runs. This cancellation does not send Completed. After the connections
are stable, send a new trigger to start again.
