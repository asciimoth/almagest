# AGENTS.md

Minimal working notes for coding agents in this repository. Use
`ARCHITECTURE.md` when you need project navigation or implementation context.

## Shape
Almagest is a Go project split into two modules:
- `lib/` (`github.com/asciimoth/almagest/lib`): reusable engine, node classes, link
  types, and workspace-facing APIs.
- `bin/` (`github.com/asciimoth/almagest/bin`): daemon, CLI, config, platform, and
  process integration.
- `panel/`: TypeScript admin web panel built into daemon static assets.

Keep the split intact. Library code must not depend on daemon/cli packages and
platform-specific logic.

## Configs
`./almagestd-config.schema.json` and `./almagestd-protected-config.schema.json`
documents almagestd configs structure. Keep them in sync when you change config
structure.

## Runtime Nodes
Check `RUNTIME_NODE_LIFECYCLE_PITFALLS.md` before adding or refactoring nodes
that manage runtime resources, background I/O, retries, link attach/detach,
config propagation, wrapper emission, or startup readiness. It documents common
failure modes around stale async completions, premature `up` state, failed
attachment reuse, and equivalent rebuild churn.

## Commands
```sh
just tidy
just vet
just fmt
just lint
just test-total
just build-web
```

## Testing

Use `just lint` and `just test-total` (they run all testing/linting logic for whole project).

## Editing Rules
- Prefer existing project patterns over new abstractions.
- Put shared runtime behavior in `lib/`.
- Put executable startup, OS integration, config files, daemon API, and CLI
  concerns in `bin/`.
- Keep panel-generated files out of git; `just build-web` writes
  `bin/almagestd/static`.
- If dependencies change, update the correct module.
