# Certcat Example Certificates

These certificate-only PEM files are generated test material for curated
`data/manual.json` examples. They are not trusted anchors.

Regenerate them from this directory:

```sh
tmpdir=$(mktemp -d)
openssl req -new -x509 -newkey rsa:2048 -nodes -days 3650 \
  -keyout "$tmpdir/warning-interception-root.key" \
  -out warning-interception-root.pem \
  -config warning-interception-root.cnf
openssl req -new -x509 -newkey rsa:2048 -nodes -days 3650 \
  -keyout "$tmpdir/narrow-scope-ca.key" \
  -out narrow-scope-ca.pem \
  -config narrow-scope-ca.cnf
openssl req -new -x509 -newkey rsa:2048 -nodes -days 3650 \
  -keyout "$tmpdir/scoped-service-anchor.key" \
  -out scoped-service-anchor.pem \
  -config scoped-service-anchor.cnf
rm -rf "$tmpdir"
```
