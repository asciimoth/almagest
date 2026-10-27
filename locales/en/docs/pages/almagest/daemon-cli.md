`almagestd` normally runs continuously as a privileged system service. The app,
web panel, and `almagest-cli` are administration clients. They can disconnect
without stopping the live graph.

## Administration CLI

Run `almagest-cli --help` to list the available surfaces. The main surfaces are:

- `graph` and `engine`, for graph inspection and changes;
- `config`, for the editable HuJSON document;
- `dns`, `kv`, `secrets`, and `tls`, for stored data;
- `snapshots`, for the create, export, import, and restore operations described
  in [Snapshots and examples](@docs/almagest/snapshots);
- `log` and `workers`, for the diagnostics described in
  [Logs and diagnostics](@docs/almagest/logs); and
- `docs`, for the documentation tree and pages.

On Unix, the default endpoint is the local administration socket. On Windows, it
is the trusted local named pipe. Use `--url` or `ALMAGEST_API_URL` to select
another endpoint.

Examples:

```sh
almagest-cli admin hello
almagest-cli config get
almagest-cli graph map
almagest-cli log watch
```

Command names and output are not a stable scripting API during pre-alpha
development. Check command help on the installed version.

## Run a configuration from standard input

`almagestd` can also work as a foreground, configuration-driven command-line
tool. Pass `--stdin` and pipe one complete HuJSON document to it:

```sh
almagestd --stdin < graph.hujson
```

The service reads the document once, starts the graph, writes logs to its normal
output, and runs until it receives a stop signal or a fatal error. This mode is
useful in a pipeline or service supervisor, in the same general style as a
socket relay tool.

The input can contain a top-level `Protected` object when the run needs
protected settings. Read [Configuration, KV, and secrets](@docs/almagest/data)
before you use this object:

```hujson
{
  "Protected": {
    "Admin": {"Unix": ""},
  },
  "nodes": {
    "Network": {"Class": "almagest/OutNetwork"},
  },
}
```

Configuration read from standard input has no file that the service can update.
The config API is therefore read-only, and graph changes cannot be persisted to
that input. Use snapshots or generate a new input document for repeatable runs.

`--stdin` and `--snapshot -` cannot both read standard input. Linux service
isolation also places additional limits on temporary storage and administration
socket options.

## Direct service options

Use `almagestd --help` for exact options. Common groups select:

- regular and protected configuration paths;
- persistent or temporary file storage;
- log level and in-memory log-cache sizes;
- local web, Unix-socket, or Windows-pipe administration endpoints;
- debug or experimental node classes;
- static TLS CRL files;
- sandbox program discovery; and
- service isolation policy and resource limits.

See [TLS](@docs/almagest/tls),
[Isolation and sandboxing](@docs/almagest/security), and
[System administration](@docs/almagest/administration) for the applicable
settings and safety limits.

Command-line and environment options can override protected file settings.
Record service overrides with the rest of the host configuration so that an
administrator can reproduce the effective startup policy.
