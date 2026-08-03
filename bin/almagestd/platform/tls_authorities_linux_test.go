//go:build linux

package platform

import (
	"bytes"
	"context"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestOSAuthorityFetcherLinuxHonorsSSLCertEnv(t *testing.T) {
	base := t.TempDir()
	bundle := filepath.Join(base, "bundle.pem")
	dirA := filepath.Join(base, "a")
	dirB := filepath.Join(base, "b")
	if err := os.Mkdir(dirA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dirB, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLinuxAuthorityTestPEM(t, bundle, []byte{1})
	writeLinuxAuthorityTestPEM(t, filepath.Join(dirA, "a.pem"), []byte{2})
	writeLinuxAuthorityTestPEM(t, filepath.Join(dirB, "b.pem"), []byte{3})

	t.Setenv(linuxCertFileEnv, bundle)
	t.Setenv(linuxCertDirEnv, dirA+":"+dirB)

	candidates, err := OSAuthorityFetcher().FetchAuthorities(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	wantDER := [][]byte{{1}, {2}, {3}}
	if len(candidates) != len(wantDER) {
		t.Fatalf("candidates len = %d, want %d", len(candidates), len(wantDER))
	}
	for i, want := range wantDER {
		if !bytes.Equal(candidates[i].DER, want) {
			t.Fatalf("candidate %d DER = %v, want %v", i, candidates[i].DER, want)
		}
	}
}

func TestLinuxFetchAuthoritiesFromSourcesStopsAfterFirstReadableBundle(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing.pem")
	first := filepath.Join(base, "first.pem")
	second := filepath.Join(base, "second.pem")
	writeLinuxAuthorityTestPEM(t, first, []byte{1})
	writeLinuxAuthorityTestPEM(t, second, []byte{2})

	candidates, err := linuxFetchAuthoritiesFromSources(context.Background(), []string{missing, first, second}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1", len(candidates))
	}
	if !bytes.Equal(candidates[0].DER, []byte{1}) {
		t.Fatalf("candidate DER = %v, want first readable bundle", candidates[0].DER)
	}
}

func TestLinuxFetchAuthoritiesFromDirSkipsSameDirectorySymlinks(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	writeLinuxAuthorityTestPEM(t, certPath, []byte{1})
	if err := os.Symlink(filepath.Base(certPath), filepath.Join(dir, "hash.0")); err != nil {
		t.Fatal(err)
	}

	candidates, err := linuxFetchAuthoritiesFromDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1", len(candidates))
	}
	if candidates[0].Source.Name != "linux:"+certPath {
		t.Fatalf("candidate source = %q, want real certificate path", candidates[0].Source.Name)
	}
}

func writeLinuxAuthorityTestPEM(t *testing.T, path string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
