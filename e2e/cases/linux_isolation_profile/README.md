# Linux isolation profile

This case runs the native launcher rejection and role-boundary tests in a
bounded cgroup. It then starts the packaged production composition and checks
the public isolation status. Other E2E cases cover native DNS providers and
process-mark routing through the same production composition.

The production composition also starts the sandbox supervisor, binds its two
IPC lanes to the restricted runtime process, and qualifies a strict Linux full
subprogram policy with a separate delegated worker cgroup.
