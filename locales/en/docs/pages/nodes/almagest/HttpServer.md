Runs a small plain-[HTTP](https://www.rfc-editor.org/rfc/rfc9110) server through
the connected [Network](@port/right/Network). Use it to test a listening path,
proxy forwarding, HTTP connectivity, or request routing. It is intended for
debugging, not for an application service.

The default listen address is `127.0.0.1:8080`. Set [Host](@port/left/Host) and
[Port](@port/left/Port) in the node menu, or supply them through their String
inputs. While a Host or Port input is connected, its menu field is read-only. An
empty host uses `127.0.0.1`. A missing, invalid, zero, or negative port from the
String input uses `8080`. Disconnecting a String input does not reset the last
value that it supplied.

The server starts only when a [Network](@port/right/Network) is connected and
supports listening. The Worker log reports when the server is waiting for
[Network](@port/right/Network), listening, or unable to listen. After a listen
or server error, the node waits one second and tries again. Disconnecting
[Network](@port/right/Network) stops the listener. Changing the host, port,
response, or [Network](@port/right/Network) restarts the listener.

Supply the response body through the [Response](@port/left/Response) String
input. If the response is empty, the server uses `Debug http server response`.
Every valid HTTP request receives status `200 OK` and the same response body,
regardless of its method, path, headers, or request body. The server does not
provide routes or custom status codes. A client must send its HTTP headers
within 5 seconds.

For each request, the node logs the method and URL, including the query string.
The Worker section keeps the latest 40 listener and request messages.

The connected [Network](@port/right/Network) determines where the listener is
created and who can reach it. With a direct host Network, `0.0.0.0` or `::` can
expose the server on every available interface. A proxy Network can expose the
listener on a different machine or address. Confirm the effective listener
location in the Network configuration and server logs.

This server has no TLS, authentication, authorization, or request filtering. Any
client that can reach it can read the configured response and generate log
entries. Query strings are also visible in the logs. Do not put secrets in the
response or URL, and do not expose this node to an untrusted network.
