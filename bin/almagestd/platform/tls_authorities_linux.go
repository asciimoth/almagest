//go:build linux

package platform

import (
	"bytes"
	"context"
	"encoding/pem"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/asciimoth/gonnect/tlspolicy"
)

const (
	linuxCertFileEnv = "SSL_CERT_FILE"
	linuxCertDirEnv  = "SSL_CERT_DIR"
)

var linuxSystemAuthorityFiles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

var linuxSystemAuthorityDirs = []string{
	"/etc/ssl/certs",
	"/etc/pki/tls/certs",
}

// OSAuthorityFetcher returns Linux certificate-authority candidates from common
// system trust bundle locations. SSL_CERT_FILE replaces the default bundle-file
// list, and SSL_CERT_DIR replaces the default directory list with colon-
// separated entries, matching Go's Unix root-store loader. The returned
// candidates are not trusted by themselves; the daemon selects them by name when
// it builds a TLS policy.
func OSAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(func(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		return linuxFetchAuthoritiesFromSources(ctx, linuxAuthorityFilesFromEnv(), linuxAuthorityDirsFromEnv())
	})
}

func linuxAuthorityFilesFromEnv() []string {
	if file := os.Getenv(linuxCertFileEnv); file != "" {
		return []string{file}
	}
	return linuxSystemAuthorityFiles
}

func linuxAuthorityDirsFromEnv() []string {
	if dir := os.Getenv(linuxCertDirEnv); dir != "" {
		return strings.Split(dir, ":")
	}
	return linuxSystemAuthorityDirs
}

func linuxFetchAuthoritiesFromSources(ctx context.Context, files, dirs []string) ([]tlspolicy.AuthorityCandidate, error) {
	var out []tlspolicy.AuthorityCandidate
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, linuxAuthorityCandidatesFromPEM(path, data)...)
		break
	}
	for _, dir := range dirs {
		candidates, err := linuxFetchAuthoritiesFromDir(ctx, dir)
		if err != nil {
			continue
		}
		out = append(out, candidates...)
	}
	return out, nil
}

func linuxFetchAuthoritiesFromDir(ctx context.Context, dir string) ([]tlspolicy.AuthorityCandidate, error) {
	entries, err := linuxReadUniqueDirectoryEntries(dir)
	if err != nil {
		return nil, err
	}
	var out []tlspolicy.AuthorityCandidate
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, linuxAuthorityCandidatesFromPEM(path, data)...)
	}
	return out, nil
}

// linuxReadUniqueDirectoryEntries is equivalent to os.ReadDir, except it
// omits hash symlinks that point to another file in the same directory. Go's
// root loader applies the same rule to avoid duplicate certificates in
// OpenSSL-style certificate directories.
func linuxReadUniqueDirectoryEntries(dir string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	unique := entries[:0]
	for _, entry := range entries {
		if !linuxIsSameDirSymlink(entry, dir) {
			unique = append(unique, entry)
		}
	}
	return unique, nil
}

func linuxIsSameDirSymlink(entry fs.DirEntry, dir string) bool {
	if entry.Type()&fs.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(filepath.Join(dir, entry.Name()))
	return err == nil && !strings.Contains(target, "/")
}

func linuxAuthorityCandidatesFromPEM(path string, data []byte) []tlspolicy.AuthorityCandidate {
	var out []tlspolicy.AuthorityCandidate
	source := tlspolicy.AuthoritySource{
		Name:  "linux:" + path,
		Trust: tlspolicy.TrustHintAnchor,
	}
	remaining := bytes.Clone(data)
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			return out
		}
		remaining = rest
		if block.Type != "CERTIFICATE" || len(block.Bytes) == 0 {
			continue
		}
		out = append(out, tlspolicy.AuthorityCandidate{
			DER:    append([]byte(nil), block.Bytes...),
			Source: source,
		})
	}
}
