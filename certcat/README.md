# Certificate Catalog

The module keeps two JSON inputs:

- `data/manual.json`: curated records with exact fingerprints, scope, actions,
  and optional sources.
- `data/machine.json`: refreshed records from public feeds. Each machine record
  must include sources with retrieval times.

The generated Go file, `catalog_gen.go`, merges both inputs and de-duplicates
records that repeat the same certificate SHA-256 or SPKI SHA-256. Manual scalar
policy fields take priority when a machine record matches the same certificate.

## Record Model

Each record describes one exact matchable certificate or public key signal:

- `kind`: `hazard_certificate`, `narrow_scope_ca`,
  `service_private_anchor`, or `threat_feed_leaf`.
- `action`: `deny`, `deny_even_if_locally_installed`, `warn`,
  `narrow_scope`, `offer_scoped_service_anchor`,
  `follow_platform_policy_or_deny`, or `enforce_platform_cutoff`.
- `recommendation`: a stable machine-readable code for UI or policy logic.
- `scope`: DNS/IP scope or service-context constraints when an anchor must not
  be used globally.

Do not identify a certificate by subject or issuer text alone. Use
`certificate_sha256` or `spki_sha256`.

## Refresh

Refresh machine data:

```sh
just certcat-fetch-force
```

Use `just certcat-fetch` to fetch only when `data/machine.json` is missing.

Generate Go data:

```sh
just certcat-generate
```

The fetcher reads these default sources:

- Mozilla CCADB intermediate report for live TLS-capable intermediates with
  X.509 DNS name constraints.
- Chromium certificate blocklist for exact certificate and SPKI blocks.
