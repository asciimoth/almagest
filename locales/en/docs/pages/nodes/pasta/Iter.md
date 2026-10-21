Ends each iteration of one ForLoop or WhileLoop. Connect the loop node's
**Loop** connector to **Loop** on Iter. This connection associates Iter with
that loop and accepts only one loop node.

Connect the end of each **Body** path to **Continue** or **Break**:

- **Continue** ends the current iteration and asks the loop to advance. A
  ForLoop starts the next in-range index or completes when its range has ended.
  A WhileLoop starts its next iteration.
- **Break** ends the current iteration and finishes the loop without another
  iteration.

The loop opens an iteration on Iter before it sends its **Index** value and **Body**
trigger. It then waits until Iter receives **Continue** or **Break**. If neither input
receives a trigger, the loop stays in its active state and does not make
progress.

For one active iteration, the first trigger received by **Continue** or **Break**
wins. Iter sends one result to the associated loop and closes that iteration.
Further triggers are ignored until the loop opens the next iteration. Each
trigger input can accept multiple trigger sources. Trigger requests are not
treated as **Continue** or **Break** events.

**Continue** and **Break** have no effect when **Loop** is disconnected or when the
associated loop has no active iteration. Disconnecting or replacing the **Loop**
connection also clears the current iteration. Triggering Iter itself has no
effect.
