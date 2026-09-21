# VTun and Spoofer UDP reply-port changes

This case verifies UDP forwarding through this graph:

`HttpClient -> VTun -> TunJoiner -> Spoofer -> OutNetwork`

The test DNS server receives each query on UDP port 53. It sends the response
from a different UDP port on the same IP address. This behavior represents
protocols that can change their remote UDP port, such as WireGuard roaming.

The HTTP request must resolve its host name and reach the local HTTP target.
The test also verifies that the DNS response port differs from the request
port. Thus, a connected Spoofer UDP socket cannot pass this test.

Run this case with:

```sh
./e2e/run.sh vtun_spoofer_udp_rebind
```
