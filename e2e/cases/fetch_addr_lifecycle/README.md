# fetch_addr_lifecycle

## Setup

The case starts a local HTTPS service on port 443. The service certificate is
valid for `api.ipify.org` and `ifconfig.co`, and the protected daemon config
contains its test CA. `/etc/hosts` maps both provider names to the service.
No public network access is used.

The service reads a control file for each request. The test can make a request
return valid provider data, an HTTP error, an invalid address, an oversized
body, or a delayed response.

## Initial Daemon Config

- `Public Network` provides the daemon system network.
- `Fetch Address` uses the `ifconfig.co` provider through that network and the
  test TLS policy.

## Expected Behavior

- The first request after startup succeeds without a request retry.
- `ifconfig.co` values are normalized and an `ipify` request clears fields that
  the provider does not supply.
- HTTP, response-validation, oversized-body, and default-TLS-fallback failures
  are visible and clear the last successful data.
- A second check while one is pending does not start another request.
- Changing the provider during a delayed request prevents the old completion
  from replacing the new result.
- Removing the network during a delayed request clears the result. Reattaching
  the same provider permits a new request, and the detached request cannot
  replace its result.
