# Snapshot archive validation

This case exports a valid daemon snapshot and derives malformed archives from
it. It verifies that snapshot import rejects unsafe entry names, invalid
metadata and config, missing or invalid databases, unsafe symlinks, unsupported
file types, and compressed size bombs. It also verifies that rejected imports do
not change the stored snapshot or stop the daemon. A conflicting startup restore
must roll back storage without replacing its active config directory.
