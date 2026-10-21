Sends a test query to an attached DNS provider. Use this node to check a DNS
chain and inspect the complete response. Connect the resolver to **DNS**.

## Request

Enter the domain in **Name** and the record type in **Type**, then select **Send
request**. A name without a final dot gets one automatically. An empty name
becomes `example.test.`. The type is not case-sensitive, and an empty type
becomes `A`.

The node supports these query types:

- `A` and `AAAA`;
- `CNAME`, `NS`, and `PTR`;
- `MX`, `TXT`, and `SRV`.

Other type names and numeric type codes are not supported. Each request is a
recursive Internet-class query with one question. The request has a fixed
15-second timeout. The node runs only one request at a time and ignores another
**Send request** action while that request is running.

## Result

**Status** starts as `ready` and changes to `running...` during a request. A DNS
response changes the status to its response code, such as `NOERROR`, `NXDOMAIN`,
`SERVFAIL`, `FORMERR`, or `NOTIMP`. Other response codes appear as `RCODE`
followed by the number. A DNS error response such as `NXDOMAIN` is a completed
request, not a query failure.

**Response** shows the question and all answer, authority, and additional
records. Each record line includes its section, name, type, TTL, and value.
Values for the supported record types are shown as text. Other record data is
shown as hexadecimal.

Connection errors, timeouts, an invalid query type, and an unavailable DNS input
set the status to `fail` and put the cause in **Error**. If the attached DNS
provider is removed or replaced, the node cancels the current request and clears
the result. The selected name and type remain saved with the node.
