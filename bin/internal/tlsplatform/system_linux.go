//go:build linux

package tlsplatform

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

// SystemAuthorityFetcher reads the Linux trust sources owned by N. Environment
// overrides are launch authority inherited by N; R cannot change them.
func SystemAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(func(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		if forceFallbackAuthorities() {
			return fallbackAuthorityCandidates(ctx)
		}
		files := []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/ca-bundle.pem", "/etc/pki/tls/cacert.pem", "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", "/etc/ssl/cert.pem"}
		dirs := []string{"/etc/ssl/certs", "/etc/pki/tls/certs"}
		if value := os.Getenv("SSL_CERT_FILE"); value != "" {
			files = []string{value}
		}
		if value := os.Getenv("SSL_CERT_DIR"); value != "" {
			dirs = strings.Split(value, ":")
		}
		var out []tlspolicy.AuthorityCandidate
		for _, path := range files {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if data, err := os.ReadFile(path); err == nil {
				out = append(out, authorityCandidatesFromPEM("linux:"+path, data)...)
				break
			}
		}
		for _, dir := range dirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if entry.Type()&fs.ModeSymlink != 0 {
					target, err := os.Readlink(filepath.Join(dir, entry.Name()))
					if err == nil && !strings.Contains(target, "/") {
						continue
					}
				}
				path := filepath.Join(dir, entry.Name())
				if data, err := os.ReadFile(path); err == nil {
					out = append(out, authorityCandidatesFromPEM("linux:"+path, data)...)
				}
			}
		}
		if len(out) != 0 {
			return out, nil
		}
		return fallbackAuthorityCandidates(ctx)
	})
}

func authorityCandidatesFromPEM(sourceName string, data []byte) []tlspolicy.AuthorityCandidate {
	var out []tlspolicy.AuthorityCandidate
	for remaining := bytes.Clone(data); len(remaining) != 0; {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type == "CERTIFICATE" && len(block.Bytes) != 0 {
			out = append(out, tlspolicy.AuthorityCandidate{DER: append([]byte(nil), block.Bytes...), Source: tlspolicy.AuthoritySource{Name: sourceName, Trust: tlspolicy.TrustHintAnchor}})
		}
	}
	return out
}
