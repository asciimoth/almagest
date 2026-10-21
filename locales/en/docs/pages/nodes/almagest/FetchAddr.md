FetchAddr checks the public address that an external service sees through the
connected **Network**. Put it after a VPN, proxy, or other network path to
confirm which public address that path uses. The node does not start a check
when the Network becomes ready.

Select one of these providers:

- `ifconfig.co` is the default. The node requests `https://ifconfig.co/json` and
  can return the public IP address, country code, city, latitude, and longitude.
- `ipify` requests `https://api.ipify.org?format=json` and returns only the
  public IP address. Its country, city, latitude, and longitude results stay
  empty or zero.

The location values are estimates supplied by the provider for the public IP
address. They are not a device location measurement. FetchAddr converts IP
addresses to their standard form, converts two-letter country codes to
uppercase, and removes spaces around city names. A missing or malformed country
code becomes an empty string. A response without a valid IP address fails the
check.

Start a check with the **Check** button, the **Check** trigger input, or the
node trigger action. A check fails if the Network is not ready. Each request has
a 15-second timeout. While a check is active, the node ignores additional check
requests.

On success, the node updates all data outputs before it sends the **Success**
trigger. On failure, it clears the data outputs and sends empty strings and zero
coordinates before it sends the **Fail** trigger. The menu shows the check
error. A newly connected data consumer receives the current value, including an
empty or zero value when no check has succeeded.

Changing the provider, TLS configuration, or Network clears the current result
and returns the node to the ready state. If a request was active, its later
result is ignored. Select a TLS configuration when the HTTPS request needs
custom certificate verification or a client certificate.

Each check contacts the selected third-party provider through the connected
Network. The provider can see and log the public source address, request time,
and HTTP metadata. The request identifies itself with the user agent
`Almagest FetchAddr`. Use a provider that meets your privacy requirements.
