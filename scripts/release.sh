#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'release: %s\n' "$*" >&2; exit 1; }
usage() {
    printf 'Usage: just release VERSION (--note TEXT | --note-file FILE | --note-auto) [--dry-run]\n'
}
version=''; note_mode=''; note=''; dry_run=false
while (($#)); do
    case $1 in
        --help|-h) usage; exit ;;
        --dry-run) dry_run=true; shift ;;
        --note|--note-file)
            [[ -z $note_mode ]] || fail 'Choose exactly one release note option.'
            (($# >= 2)) || fail "$1 needs a value."
            note_mode=$1; note=$2; shift 2 ;;
        --note-auto)
            [[ -z $note_mode ]] || fail 'Choose exactly one release note option.'
            note_mode=$1; shift ;;
        -*) fail "Unknown option: $1" ;;
        *) [[ -z $version ]] || fail 'Only one version is allowed.'; version=${1#v}; shift ;;
    esac
done
[[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || fail 'Version must be x.y.z (an optional v prefix is allowed).'
IFS=. read -r major minor patch <<< "$version"
((${#major} <= 3 && ${#minor} <= 3 && ${#patch} <= 5)) || fail 'Version exceeds MSI limits.'
((major <= 255 && minor <= 255 && patch <= 65535)) || fail 'Version exceeds MSI limits (255.255.65535).'
[[ -n $note_mode ]] || fail 'Pass --note TEXT, --note-file FILE, or --note-auto explicitly.'
if [[ $note_mode == --note-file ]]; then
    [[ -f $note && -r $note ]] || fail "Cannot read release notes: $note"
    note=$(cat -- "$note")
fi
[[ $note_mode == --note-auto || $note =~ [^[:space:]] ]] || fail 'Release notes must not be empty.'
if ! $dry_run; then
    [[ -t 0 ]] || fail 'Run from an interactive terminal to approve signing after packaging.'
fi
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$repo_root"
# Git and GoReleaser use the Go tag format. The MSI builder gets the numeric
# version below because Windows package versions cannot have a v prefix.
tag=v$version
fingerprint=C5C84658CCFD7E8E71DEE933AF3AE54FC3A35C9F
gpg_command=${GPG_COMMAND:-gpg}
for tool in git just goreleaser gh "$gpg_command" go gcc pkg-config docker node npm cargo \
    golangci-lint markdownlint ripsecrets pwsh python3 shellcheck actionlint typos jq \
    sha256sum flock; do
    command -v "$tool" >/dev/null || fail "Missing command: $tool (enter nix develop)."
done
# Prevent two release processes from changing the same tag or dist directory.
exec 9>"$(git rev-parse --git-path almagest-release.lock)"
flock -n 9 || fail 'Another release process is active.'
[[ $(go env GOHOSTOS) == linux && $(go env GOHOSTARCH) == amd64 ]] || fail 'Packaging requires a linux/amd64 host.'
[[ -z $(git status --porcelain) ]] || fail 'Commit all changes before releasing.'
[[ $(git rev-parse --is-shallow-repository) == false ]] || fail 'Fetch the full Git history and tags before releasing.'
head=$(git rev-parse HEAD)
git var GIT_COMMITTER_IDENT >/dev/null
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then fail "Local tag $tag already exists."; fi
# Use origin for both Git and GitHub operations, even if GH_REPO is set.
origin=$(git remote get-url --push --all origin)
[[ $origin != *$'\n'* ]] || fail 'origin must have exactly one push URL.'
case $origin in
    https://github.com/*) github_repo=${origin#https://github.com/} ;;
    git@github.com:*) github_repo=${origin#git@github.com:} ;;
    ssh://git@github.com/*) github_repo=${origin#ssh://git@github.com/} ;;
    *) fail 'origin must point to a github.com repository.' ;;
esac
github_repo=${github_repo%.git}
[[ $github_repo =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail 'Invalid GitHub origin.'
remote_tags=$(git ls-remote --tags "$origin" "refs/tags/$tag")
[[ -z $remote_tags ]] || fail "Remote tag $tag already exists."
export GH_HOST=github.com
export GITHUB_TOKEN=${GITHUB_TOKEN:-${GH_TOKEN:-}}
if [[ -z $GITHUB_TOKEN ]]; then GITHUB_TOKEN=$(gh auth token --hostname github.com); fi
[[ -n $GITHUB_TOKEN ]] || fail 'Set GITHUB_TOKEN or GH_TOKEN with contents:write access.'
export GH_TOKEN=$GITHUB_TOKEN
permissions=$(gh api "repos/$github_repo" --jq '.permissions.push')
[[ $permissions == true ]] || fail 'GitHub credentials do not have repository write access.'
releases=$(gh api --paginate "repos/$github_repo/releases" --jq '.[].tag_name')
while IFS= read -r existing; do [[ $existing != "$tag" ]] || fail "Release $tag already exists."; done <<< "$releases"
git push --dry-run origin "HEAD:refs/tags/$tag"
"$gpg_command" --list-secret-keys "$fingerprint" >/dev/null
[[ -r packaging/keys/ascii-moth.asc ]] || fail 'Release public key is absent.'
public_fingerprint=$("$gpg_command" --batch --with-colons --show-keys packaging/keys/ascii-moth.asc |
    awk -F: '$1 == "fpr" && !found {print $10; found=1}')
[[ $public_fingerprint == "$fingerprint" ]] || fail 'Release public key does not match the signing key.'
[[ -n ${PSSCRIPTANALYZER_PATH:-} && -e $PSSCRIPTANALYZER_PATH ]] || fail 'Set PSSCRIPTANALYZER_PATH to the PowerShell analyzer module.'
# PowerShell expands its own environment variable.
# shellcheck disable=SC2016
pwsh -NoLogo -NoProfile -NonInteractive -Command 'Import-Module $env:PSSCRIPTANALYZER_PATH -ErrorAction Stop'
pkg-config --exists libseccomp gtk+-3.0 webkit2gtk-4.1
docker info >/dev/null
docker buildx version >/dev/null
cargo clippy --version >/dev/null
if [[ ${RUN_WINVM_TESTS:-0} == 1 ]]; then
    command -v ruff >/dev/null || fail 'ruff is required when RUN_WINVM_TESTS=1.'
fi
goreleaser check
./scripts/release-winvm-preflight.sh
if [[ -n ${GHREPO_REPO_DIR:-} ]]; then
    command -v ghrepo >/dev/null || fail 'ghrepo is required when GHREPO_REPO_DIR is set.'
    [[ -d $GHREPO_REPO_DIR && -w $GHREPO_REPO_DIR && -r $GHREPO_REPO_DIR/ghrepo.yaml ]] || fail 'GHREPO_REPO_DIR must be a writable, initialized ghrepo repository.'
    ghrepo check --dry-run
fi
printf 'Preflight passed for %s in %s.\n' "$tag" "$github_repo"
if $dry_run; then
    printf '%s\n' 'Dry run: would run lint, create a local tag, build packages, notify and wait for signing approval, sign, push the tag, and publish a GitHub Release.'
    [[ -z ${GHREPO_REPO_DIR:-} ]] || printf 'Would add published DEB/RPM URLs to %s with ghrepo.\n' "$GHREPO_REPO_DIR"
    exit
fi
just lint actionlint shellcheck typos
[[ -z $(git status --porcelain) && $(git rev-parse HEAD) == "$head" ]] || fail 'Checks changed the source. Commit changes before releasing.'
mkdir -p build/packaging
notes_file=$repo_root/build/packaging/release-notes.md
notes_args=()
if [[ $note_mode != --note-auto ]]; then
    printf '%s\n' "$note" > "$notes_file"
    notes_args=(--release-notes "$notes_file")
fi
# Keep the tag local until all assets are signed. Keep it on failure for diagnosis.
git -c tag.gpgSign=false tag -a "$tag" "$head" -m "Release $tag"
release_state='local tag created'
report_exit() {
    local status=$?
    if ((status != 0)); then
        printf 'Release stopped (%s). Tag %s and build outputs are retained; inspect them before retrying.\n' \
            "$release_state" "$tag" >&2
    fi
}
trap report_exit EXIT
just build-web
rm -f build/packaging/signing-inputs.sha256
./packaging/build-native.sh
./packaging/build-app.sh
# An inherited tag override must not select a different version for packages.
GORELEASER_CURRENT_TAG=$tag goreleaser release --clean --skip=sign,publish --timeout=4h "${notes_args[@]}"
if [[ $note_mode == --note-auto ]]; then
    [[ -s dist/CHANGELOG.md ]] || fail 'GoReleaser did not produce release notes.'
    cp dist/CHANGELOG.md "$notes_file"
fi
./scripts/prune-package-output.sh dist
install -m 0644 packaging/keys/ascii-moth.asc dist/ascii-moth.asc
./scripts/package-windows.sh "$version"
[[ -z $(git status --porcelain) && $(git rev-parse HEAD) == "$head" ]] || fail 'Source changed during packaging; refusing to sign.'
export ALMAGEST_SIGNING_MANIFEST=$repo_root/build/packaging/signing-inputs.sha256
ALMAGEST_SIGNING_MESSAGE='Return to the release terminal, type sign, and press Enter to sign.' \
    ./scripts/package-signing.sh prepare dist
# Keep an explicit upload list before waiting for the user. Do not upload
# unrelated files which another command may leave in dist later.
mapfile -t artifacts < <(sed -n 's/^[[:xdigit:]]\{64\}  //p' "$ALMAGEST_SIGNING_MANIFEST")
assets=(dist/ascii-moth.asc)
urls=()
for artifact in "${artifacts[@]}"; do
    assets+=("dist/$artifact" "dist/$artifact.sig")
    case $artifact in
        *.deb|*.rpm) urls+=("https://github.com/$github_repo/releases/download/$tag/$artifact") ;;
    esac
done
printf 'Packaging is complete. Type sign and press Enter to request hardware signing: '
read -r approval || fail 'No signing approval received.'
[[ $approval == sign ]] || fail "Signing cancelled. Local tag $tag and unsigned assets are retained."
./scripts/package-signing.sh sign dist
./scripts/package-signing.sh verify dist
[[ -z $(git status --porcelain) && $(git rev-parse HEAD) == "$head" ]] || fail 'Source changed during packaging; refusing to publish.'
[[ $(git rev-parse "$tag^{commit}") == "$head" ]] || fail 'Release tag changed during packaging.'
git push origin "refs/tags/$tag"
release_state='tag pushed; GitHub Release may be incomplete'
# Publish the signed files without a second build. No binaries can change here.
gh release create "$tag" "${assets[@]}" --repo "github.com/$github_repo" --verify-tag --title "$tag" --notes-file "$notes_file"
release_state='GitHub Release published; local ghrepo update may be incomplete'
if [[ -n ${GHREPO_REPO_DIR:-} ]]; then
    ((${#urls[@]} > 0)) || fail 'No DEB/RPM packages found.'
    ghrepo add "${urls[@]}"
fi
printf 'Published https://github.com/%s/releases/tag/%s\n' "$github_repo" "$tag"
