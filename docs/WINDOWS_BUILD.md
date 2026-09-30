# Windows cross-builds

Use Go 1.26.6 or a newer patch release in the Go 1.26 series or later. Node.js
and npm are also required because the daemon embeds the admin panel.

Build both supported Windows architectures:

```sh
just build-windows
```

Build one architecture:

```sh
just build-windows-arch amd64
just build-windows-arch arm64
```

The build creates `go.work` when it is absent. It regenerates the certificate
catalog and the embedded panel before Go package discovery. It then does these
checks and builds:

1. Verify each module and check `go mod tidy -diff`.
2. Discover packages with `GOOS=windows`, the selected `GOARCH`, and the
   selected build tags.
3. Build all packages in `certcat`, `lib`, and `bin`.
4. Compile each package test binary. Do this for the default build and the
   `almagest_e2e` integration build.
5. Build `almagestd.exe` and `almagest-cli.exe`.

The command writes ignored output to `artifacts/windows`. Set
`WINDOWS_BUILD_DIR` to use a different output directory. Each architecture has
a `build-evidence.json` file. Test compilation and test execution have separate
fields because a Linux cross-build does not execute Windows tests. Package
manifests show the exact target-selected package set. The shared
`generated-inputs.sha256` file records every tracked input to the certificate
catalog and panel generators.

The cross-build is a portability check. It is not native Windows qualification.
