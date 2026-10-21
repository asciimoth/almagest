Serves configured DNS records and optionally sends misses to one **Fallback** DNS
service. The DNS output is available even when no records or fallback are
configured.

Write one record on each line in this form:

```text
NAME TYPE VALUE
```

Blank lines and lines that start with `#` are ignored. Name matching is not
case-sensitive. A final dot is optional in configured names and
domain-name values.

These record types and value formats are supported:

- `A IPV4_ADDRESS`
- `AAAA IPV6_ADDRESS` (`AAA` is also accepted as an alias for `AAAA`)
- `CNAME NAME`, `NS NAME`, or `PTR NAME`
- `TXT TEXT`
- `MX PREFERENCE HOST`
- `SRV PRIORITY WEIGHT PORT TARGET`

MX preferences and all three SRV numbers must be from 0 through 65535. TXT uses
all text after the type. Double quotes can group text that contains spaces. A
backslash escapes the next character inside quoted text.

For example:

```spacetable
# Local service records
app.example.test A 192.0.2.10
app.example.test AAAA 2001:db8::10
alias.example.test CNAME app.example.test
example.test MX 10 mail.example.test
_https._tcp.example.test SRV 10 5 443 app.example.test
example.test TXT "managed locally"
10.2.0.192.in-addr.arpa PTR app.example.test
```

You can add multiple records with the same name and type. A matching question
returns all of them. Matching is exact by both name and record type. The node
does not follow CNAME records, synthesize other types, or use wildcard names.

If no exact record matches, the node sends the complete request to **Fallback**.
For example, an AAAA question is a miss when only an A record exists for the
name. Without Fallback, a miss returns NXDOMAIN. For a request with multiple
questions, all questions must match static records. If one question misses,
the complete request goes to **Fallback** and static answers are not combined with
the fallback response.

**TTL** sets the lifetime, in seconds, for all static records. The default is 60,
and 0 is permitted. Fallback responses keep the TTL values supplied by the
fallback service.

**TTL** must be a nonnegative integer. An invalid record edit is not applied. The
menu identifies the line that contains the invalid record, and the last valid
records continue to serve requests.
