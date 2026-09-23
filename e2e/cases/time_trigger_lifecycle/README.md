# Time Trigger Lifecycle

This hermetic case connects `TimeTrigger` nodes to protected command nodes. The
commands append fixed markers to files, which makes trigger delivery visible
without network access or clock-dependent daily scheduling.

Assertions cover these situations:

- an interval timer starts one tracked worker and emits repeated triggers;
- an invalid edit keeps the last valid schedule active;
- disabling the timer stops its worker and further trigger delivery;
- repeated interval changes converge on one current worker;
- removing the output link stops delivery while the timer remains active;
- restoring the link resumes delivery without rebuilding either node;
- `emitOnReady` emits once for a disabled timer; and
- daemon restart restores the interval schedule and emits exactly one new
  ready event.

The protected commands contain fixed test-only shell programs and accept no
runtime slots. The case uses the debug system and does not require privileged
Docker networking.
