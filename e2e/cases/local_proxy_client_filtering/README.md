# LocalProxy client filtering

This case runs in direct (`off`), `auto`, and `strict` daemon isolation modes.
It verifies that `LocalProxy`:

- keeps the legacy allow-all behavior when no client rules are configured;
- permits `curl` and rejects a renamed client with a sysnet-linux `comm` rule;
- permits a selected local user and rejects another user with a sysnet-linux
  `user` rule;
- restores allow-all behavior after the rules are removed; and
- applies the same matcher behavior through the isolated network broker.
