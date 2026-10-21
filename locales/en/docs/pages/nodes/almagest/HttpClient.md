Sends an HTTP or HTTPS request through a connected **Network**. Use this node to
test a route or examine the response from a service.

## Request

Connect a **Network** before you send a request. The node uses this network for
the complete connection, including name resolution.

Set these request values:

- **URL**: The destination URL. Use an `http://` or `https://` URL. The default
  is `http://127.0.0.1:8080/`.
- **Method**: The HTTP method. The node removes surrounding spaces and changes
  the value to uppercase. An empty value becomes `GET`.
- **Body**: The request body. The node sends this text without changes.

You can connect String values to **URL**, **Method**, and **Body**. A connected
value replaces the value in the menu, and the related field becomes read-only.

This node does not let you set request headers yet. Select **Send request** to
start the request. Only one request can run at a time. Each request has a fixed
15-second timeout.

## HTTPS

For an HTTPS URL, select the TLS configuration that contains the required trust
settings and client certificate settings. Certificate verification uses the
hostname in the URL unless the selected configuration specifies a different
server name.

An invalid TLS configuration stops an HTTPS request. It does not affect an HTTP
request because HTTP does not use TLS.

## Result

The status is `ready` before a request and `running...` during a request. After
a response, the status shows the HTTP status, such as `200 OK`. The **Response**
field contains the complete response body. It does not contain the response
headers.

An HTTP error status, such as `404 Not Found`, is still a completed response.
Connection, TLS, timeout, URL, and response-read failures set the status to
`fail` and put details in **Error**.

The node reads the complete response body into memory. Use it for diagnostic
responses, not for large downloads.

If you disconnect or replace the Network, or change the TLS configuration, the
node cancels an active request and clears the previous result.
