# flags_storage

Runs with daemon isolation off, auto, and strict. Checks missing and existing
flags.json at startup, CLI read and replacement, snapshot inclusion, and live
and startup restore state. A snapshot without flags.json keeps the live file.
Also checks protected and CLI flag overrides on reads and writes in each mode.
