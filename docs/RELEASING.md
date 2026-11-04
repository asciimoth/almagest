# Release Almagest

Run releases from a clean, committed checkout on Linux amd64. Enter
`nix develop` first. Fetch the full Git history and tags. Set `origin` to the
GitHub repository where the release must be published.

Pass release notes explicitly:

```sh
just release 1.2.3 --note 'Describe the changes in this release.'
just release 1.2.3 --note-file /path/to/release-notes.md
```

Use `--note-auto` to use GoReleaser's built-in Git changelog generation:

```sh
just release 1.2.3 --note-auto
```

Versions can have a `v` prefix. The command always creates the Git tag `v1.2.3`
for version `1.2.3`, and GitHub Releases uses that tag. It passes `1.2.3` to the
Windows MSI builder. The numeric version must be within MSI limits:
`255.255.65535`. Prerelease and build suffixes are not supported because all
releases include the Windows MSI.

## Prerequisites

- Set `GITHUB_TOKEN` or `GH_TOKEN` with contents write access to `origin`.
  Otherwise, the command reads the token from `gh auth token`.
- Configure Git push access to `origin` and a Git committer identity.
- Make the signing key `C5C84658CCFD7E8E71DEE933AF3AE54FC3A35C9F` available to
  GPG. This is the key used by `just sign-package`. `GPG_COMMAND` can select
  another GPG executable.
- Start Docker. Install its buildx plugin.
- Prepare the Windows VM with `just winvm-image`. The release checks KVM,
  firmware, the cached image, the SSH key pair, available memory, at least 10
  GiB of free artifact space, and the Windows driver and license inputs. Unset
  `WINVM_PREPARED_DIR`; a release must build the MSI from current source.
- Keep the development shell variables `PSSCRIPTANALYZER_PATH`,
  `SYSNET_WINDOWS_WINTUN_DIR`, and `MULLVAD_SPLIT_TUNNEL_DRIVER_DIR` set.
- Use an interactive terminal. Signing needs explicit input after packaging.

The command checks arguments, notes, tools, authentication, repository access,
existing tags and releases, the signing key, package dependencies, GoReleaser
configuration, and the Windows image before it starts lint or builds. These
checks cannot prevent later network failures, hardware failures, or changes to
remote permissions.

## Release steps

1. Run `just lint actionlint shellcheck typos`. Do not run `just check` or the
   full repository test suite. The lint recipe keeps its existing prerequisites.
1. Create the local annotated tag. Build the panel, native launchers, desktop
   app, Linux packages, and Windows MSI with the release version.
1. Prepare the signing manifest and show the package-ready notification. The
   terminal then waits. Type `sign` and press Enter to start hardware signing.
   Any other input cancels the release before signing or publishing.
1. Sign the artifacts and verify their checksums and signatures. Push the tag
   and upload only the prepared artifacts, signatures, and public key to GitHub
   Releases. There is no second build after signing.
1. If `GHREPO_REPO_DIR` is set, use `ghrepo add` with the published DEB and RPM
   asset URLs. The local repository must already be initialized and pass
   `ghrepo check --dry-run`. Run `ghrepo publish` separately when ready to
   publish package repository metadata to GitHub Pages.

The normal snapshot workflow remains available through `just package` and
`just sign-package`.

## Dry run and failures

```sh
just release 1.2.3 --note-auto --dry-run
```

Dry run checks the prerequisites and prints the release plan. It does not run
lint, build packages, create a tag, sign, upload, or add packages to ghrepo. It
still needs valid credentials and build prerequisites. Remote checks use the
network; the ghrepo check uses a temporary repository copy.

After a build, signing, or publishing failure, the command keeps the local tag
and build outputs for inspection. It does not automatically delete a tag or
release. If the tag has not been pushed, remove that local tag before starting
again. If it has been pushed, inspect the GitHub release state and finish or
repair that release manually. Do not move a published tag to another commit. The
failure message states whether the tag is local, the tag was pushed, or the
GitHub Release was published. If only the ghrepo update failed, retry
`ghrepo add` with the published DEB and RPM asset URLs; do not rebuild the
release.
