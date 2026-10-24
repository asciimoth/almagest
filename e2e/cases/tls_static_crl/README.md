# Static TLS CRLs

Runs in both direct and automatic isolation modes. The case loads one protected
CRL file that revokes an OS root. It verifies read-only status, separate CRL
provenance, default-policy omission, CLI output without raw CRL data, and
one-time file loading. In isolated mode, only trusted bootstrap reads the file;
the restricted runtime receives literal PEM bytes.
