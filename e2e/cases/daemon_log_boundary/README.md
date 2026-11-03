# Daemon log boundary

The admin log subscription must include safe daemon readiness entries in both
direct and isolated modes. Operator output before and after the admin cache is
created stays in the process log and is not copied into the admin API. The case
checks storage path and version messages as examples.
