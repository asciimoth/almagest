# Subprogram contracts

Runs all nine combinations of daemon isolation `off|auto|strict` and subprogram
sandboxing `off|auto|strict`. Each combination starts the production daemon and
uses the debug sandbox probe node to start a fixed static process. The case
checks the selected profiles and child identity through the admin API. It also
runs the portable sandbox contract, policy planner, owner manager, direct
backend, and durable service tests once as root and once as an ordinary user.
The native tests cover strict feature status, namespaces, the mount view, mapped
inputs, cgroups, descendant cleanup, private TCP, and pathname Unix sockets.
