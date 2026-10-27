Almagest uses small restricted processes to reduce the effect of a compromised
component. It also runs approved third-party programs through a separate sandbox
service. These protections are work in progress, have performance and memory
overhead, and have not had an independent security audit.

Service isolation and subprogram sandboxing are independent. Enabling one does
not enable the other.

## Service isolation

Service isolation separates privileged host networking, the graph runtime,
configuration writes, and approved external commands. Each part receives only
the paths, handles, and operations assigned to its role.

Select the startup policy with one of these options:

```sh
almagestd --isolation=strict
almagestd --isolation=auto
almagestd --isolation=off
```

- `strict` requires the complete qualified isolation profile. Startup fails if a
  required protection is unavailable.
- `auto` selects a compatible qualified profile. It does not silently fall back
  to a less restricted direct service after a selected isolated launch fails.
- `off` runs the service directly. `--no-isolation` also selects direct mode.

Current packaged services use `off` by default because isolation still has
overhead and deployment limits. Linux amd64 has the complete implemented
profile. Other architectures and platforms have partial components or direct
mode only. Windows service isolation is not released as a complete installed
mode. [Service and command line](@docs/almagest/daemon-cli) describes where
startup options apply.

## Subprogram sandboxing

The sandbox service is for third-party programs that a node must start. A node
can select only a protected template and named program. It cannot select a host
executable path, raw command line, credentials, or arbitrary host files.

Protected `Sandbox.Mode` has three values:

- `strict` requires all mandatory protections in the qualified OS profile.
- `auto` applies compatible protections and reports omitted protections.
- `off` applies no native child confinement, but still enforces named programs,
  templates, ownership, bounded operations, and process tracking.

The sandbox default is `auto`, so it uses available protections. Full Linux
sandboxing is qualified on amd64. Other Linux architectures and Windows have
partial support. Windows can satisfy its reviewed native profile while still
lacking an exact filesystem view and private network.

Sandbox policy is protected configuration. Read
[Configuration, KV, and secrets](@docs/almagest/data) for its access and restart
rules.

Use strict mode when missing protection must stop the program. In auto mode,
inspect the reported effective profile and warnings. A successful launch does
not mean that every possible protection was applied.

## Protected sandbox option reference

The top-level `Sandbox` object has these service settings:

- `Mode` selects `off`, `auto`, or `strict`.
- `DiscoverSystemPrograms` adds supported programs found in the service `PATH`.
  Explicit entries take priority.
- `TemporaryRoot` sets the root for temporary sandbox data.
- `RegistryRoot` sets the root for sandbox registry data.
- `LinuxLauncher` sets the trusted Linux sandbox-launcher path.
- `LinuxCgroupRoot` sets the delegated cgroup v2 subtree for full Linux
  sandboxes.
- `Programs` defines the approved programs that templates can use.
- `Templates` defines the profiles that node classes can select.
- `Limits` sets service-wide limits for sandbox work.

`Sandbox.Limits` contains four limits. `Instances` limits active sandbox
instances. `Commands` limits active commands. `RelayBytes` limits data held by
relays. `Receipts` limits retained command receipts.

Each `Programs` entry describes one fixed command shape:

- `Executable` sets the fixed host executable.
- `Builder` selects the command builder. The current value is `fixed-v1`.
- `Arguments` lists fixed and input-derived arguments in order.
- `Inputs` defines the named values that a node must supply.
- `Descendants` lists other program IDs that this program can start.

Each `Arguments` item contains either `Literal` for fixed text or `Input` for a
named input. An `Inputs` entry uses `Kind` to select `text`, `integer`,
`boolean`, `path`, `program`, or `endpoint`. `Required` must be true. `Maximum`
limits the accepted input size.

Each `Templates` entry controls where and how approved programs run:

- `AllowedClasses` lists the node classes that can select the template.
- `Programs` lists the approved program IDs available in the template.
- `RuntimeProfile` selects `direct-v1`, `linux-landlock-v1`, or
  `windows-native-v1`.
- `PersistentRoot` sets the writable persistent directory for the template.
- `DeleteStateOnOwnerRemoval` deletes persistent state when its owning node is
  removed.
- `RequiredFeatures` lists protections that the effective profile must provide.
- `ReadOnly` maps approved host files into the sandbox.
- `Environment` controls inherited and fixed environment variables.
- `Linux` selects the Linux identity for the sandbox.
- `Limits` sets resource and operation limits for this template.

`RequiredFeatures` can require privilege removal, environment control, handle
inheritance control, writable storage, filesystem access, an exact filesystem
view, a private network, process isolation, process-tree control, or resource
limits.

Each `ReadOnly` item uses `Source` for the host path and `Target` for its
absolute sandbox path. `Executable` permits execution from that mapping. In
`Environment`, `Inherit` lists variables copied from the service environment,
and `Set` supplies fixed values.

`Linux.IdentityPool` selects a configured identity pool. As an alternative,
`UID` and `GID` select one fixed identity and must be set together.
`CurrentUser` uses the service identity.

Template `Limits` has these fields:

- `Commands`, `Tasks`, `Files`, and `Sockets` limit active operations, processes
  and threads, open files, and sockets.
- `MemoryBytes`, `CPUPercent`, `TransferBytes`, `TemporaryBytes`, and
  `OutputBytes` limit memory, CPU, transferred data, temporary storage, and
  captured output.
- `Required` lists the resource limits that must be enforced. It can contain
  `Tasks`, `Files`, `MemoryBytes`, `CPUPercent`, and `TemporaryBytes`.

## TorDriver example

[TorDriver](@docs/node/almagest/TorDriver) is a graph node that uses the sandbox
service. The node supplies Tor configuration and a Network path. Protected
policy selects the Tor executable, fixed argument shape, allowed node class,
identity, files, and resource limits.

Automatic discovery is the preferred packaged configuration. It finds Tor and
its local runtime files without executing Tor during discovery:

```hujson
{
  "Sandbox": {
    "Mode": "auto",
    "DiscoverSystemPrograms": true,
  },
}
```

An administrator can disable discovery and define a direct profile explicitly.
This minimal Linux package example fixes the executable and arguments but does
not provide native filesystem or network confinement. Change the state paths if
the service uses another storage layout:

```hujson
{
  "Sandbox": {
    "Mode": "auto",
    "DiscoverSystemPrograms": false,
    "TemporaryRoot": "/var/lib/almagest/store.subprogram-sandbox/temporary",
    "RegistryRoot": "/var/lib/almagest/store.subprogram-sandbox/registry",
    "Programs": {
      "tor": {
        "Executable": "/usr/bin/tor",
        "Builder": "fixed-v1",
        "Inputs": {
          "defaults-torrc": {"Kind": "path", "Required": true},
          "torrc": {"Kind": "path", "Required": true},
        },
        "Arguments": [
          {"Literal": "--defaults-torrc"}, {"Input": "defaults-torrc"},
          {"Literal": "-f"}, {"Input": "torrc"},
        ],
      },
    },
    "Templates": {
      "tor": {
        "AllowedClasses": ["almagest/TorDriver"],
        "Programs": ["tor"],
        "RuntimeProfile": "direct-v1",
      },
    },
  },
}
```

For native confinement, replace the direct profile with a supported platform
profile and declare all required read-only runtime files, identity settings,
storage, and limits. Dynamic executables can need an exact set of loaders,
libraries, and data files. Verify the effective sandbox status after each
change. Do not copy a runtime-file list from a different OS release.

## Limits of the boundary

Isolation does not protect against an operating-system kernel defect or an
administrator. A sandbox cannot prevent data transfer through a Network that the
graph intentionally gives to the program. Persistent sandbox state also stays
outside [graph snapshots](@docs/almagest/snapshots).

Treat `auto` status, startup warnings, and profile changes as security-relevant
events. Use `strict` and test startup on the deployed platform when a missing
feature must fail closed.
