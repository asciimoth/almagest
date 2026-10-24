# Local proxy secret with curl

This case starts an authenticated LocalProxy connected to OutNetwork. The
configured inline password is an incorrect fallback, and a protected secret
contains the initial accepted password.

The case uses curl as an independent SOCKS5 client. It verifies the initial
protected secret and then uses the daemon secrets API to rotate, rename, delete,
and recreate a mutable password secret. Successful states send HTTP traffic
through LocalProxy. Failure states prove that stale or overridden passwords are
not accepted.
