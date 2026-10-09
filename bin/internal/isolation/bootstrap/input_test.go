package bootstrap

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
)

type memoryLoader struct {
	trusted      map[string][]byte
	unsafe       map[string][]byte
	trustedReads []string
	unsafeReads  []string
}

func (l *memoryLoader) ReadTrusted(path string, limit int64) ([]byte, error) {
	l.trustedReads = append(l.trustedReads, path)
	return memoryRead(l.trusted[path], limit)
}

func (l *memoryLoader) ReadUntrusted(path string, limit int64) ([]byte, error) {
	l.unsafeReads = append(l.unsafeReads, path)
	return memoryRead(l.unsafe[path], limit)
}

func memoryRead(data []byte, limit int64) ([]byte, error) {
	if data == nil {
		return nil, os.ErrNotExist
	}
	if int64(len(data)) > limit {
		return nil, ErrInputTooLarge
	}
	return bytes.Clone(data), nil
}

func TestLoadConfigKeepsUnsafeFlagAwayFromProtectedInput(t *testing.T) {
	loader := &memoryLoader{
		trusted: map[string][]byte{"protected": []byte(`{"System":{"Type":"native"}}`)},
		unsafe:  map[string][]byte{"regular": []byte(`{"nodes":{}}`)},
	}
	inputs, err := LoadConfig(ConfigSources{
		RegularPath: "regular", ProtectedPath: "protected", UnsafeRegular: true,
	}, loader)
	if err != nil {
		t.Fatal(err)
	}
	if string(inputs.Regular) != `{"nodes":{}}` || len(loader.unsafeReads) != 1 || loader.unsafeReads[0] != "regular" {
		t.Fatalf("regular input or reads = %q, %#v", inputs.Regular, loader.unsafeReads)
	}
	if len(loader.trustedReads) != 1 || loader.trustedReads[0] != "protected" {
		t.Fatalf("trusted reads = %#v, want only protected", loader.trustedReads)
	}
}

func TestLoadCombinedStdinExtractsOneProtectedMember(t *testing.T) {
	inputs, err := LoadConfig(ConfigSources{
		CombinedStdin: true,
		Stdin: strings.NewReader(`{
			// trusted local launch document
			"nodes": {},
			"Protected": {"Secrets":{"token":{"Value":"secret"}}}
		}`),
	}, &memoryLoader{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(inputs.Regular, []byte("Protected")) || bytes.Contains(inputs.Regular, []byte("secret")) {
		t.Fatalf("regular input retained protected data: %s", inputs.Regular)
	}
	if !bytes.Contains(inputs.Protected, []byte("secret")) {
		t.Fatalf("protected input = %s", inputs.Protected)
	}

	_, err = LoadConfig(ConfigSources{CombinedStdin: true, Stdin: strings.NewReader(
		`{"Protected":{},"Protected":{"System":{}}}`,
	)}, &memoryLoader{})
	if !errors.Is(err, ErrAmbiguousInput) {
		t.Fatalf("duplicate Protected error = %v", err)
	}
}

func TestLoadCombinedStdinUsesDefaultsAndBoundsInput(t *testing.T) {
	inputs, err := LoadConfig(ConfigSources{
		CombinedStdin: true, Stdin: strings.NewReader(""),
		DefaultRegular: []byte(`{"nodes":{}}`), DefaultProtected: []byte(`{"System":{}}`),
	}, &memoryLoader{})
	if err != nil || string(inputs.Regular) != `{"nodes":{}}` || string(inputs.Protected) != `{"System":{}}` {
		t.Fatalf("default inputs = %#v, %v", inputs, err)
	}
	_, err = LoadConfig(ConfigSources{
		CombinedStdin: true, Stdin: io.LimitReader(zeroReader{}, MaxProtectedInputBytes+1),
	}, &memoryLoader{})
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized stdin error = %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = 'x'
	}
	return len(buffer), nil
}

func TestResolveProtectedFreezesExactSecretAndTLSInputs(t *testing.T) {
	loader := &memoryLoader{trusted: map[string][]byte{
		"/trusted/ca.pem":  []byte("certificate-file"),
		"/trusted/key.pem": []byte("built-in-keypair"),
	}}
	data := []byte(`{
		"Admin": {"Web": 1},
		"Secrets": {
			"inline": {"Value":"literal", "Nodes":["one"]},
			"from-env": {"Env":"TOKEN", "Nodes":["two"]}
		},
		"TLS": {
			"Certificates": {
				"inline": {"PEM":"certificate-inline"},
				"file": {"File":"/trusted/ca.pem"}
			},
			"Policies": {"default":{"Roots":["file"]}},
			"BuiltinCA": {"File":"/trusted/key.pem"}
		}
	}`)
	resolved, err := ResolveProtected(data, loader, func(name string) (string, bool) {
		return map[string]string{"TOKEN": "environment-secret"}[name], name == "TOKEN"
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Digest == [32]byte{} || resolved.Runtime.Secrets["from-env"].Value != "environment-secret" ||
		resolved.Runtime.Secrets["from-env"].Source != "environment" ||
		resolved.Runtime.Certificates["file"] != "certificate-file" || resolved.Runtime.BuiltinCA != "built-in-keypair" {
		t.Fatalf("resolved protected inputs = %#v", resolved)
	}
	if strings.Contains(strings.Join(loader.trustedReads, " "), "TOKEN") {
		t.Fatalf("environment selector reached file loader: %#v", loader.trustedReads)
	}
	for _, forbidden := range []string{"TOKEN", "/trusted/ca.pem", "/trusted/key.pem"} {
		if strings.Contains(resolved.Runtime.Secrets["from-env"].Value+resolved.Runtime.Certificates["file"]+resolved.Runtime.BuiltinCA, forbidden) {
			t.Fatalf("runtime values contain selector %q", forbidden)
		}
	}
}

func TestResolveProtectedRejectsAmbiguousMissingAndOversizedInputs(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"duplicate", `{"Secrets":{"a":{"Value":"x"}},"Secrets":{}}`},
		{"both-secret-sources", `{"Secrets":{"a":{"Value":"x","Env":"TOKEN"}}}`},
		{"neither-secret-source", `{"Secrets":{"a":{}}}`},
		{"invalid-environment", `{"Secrets":{"a":{"Env":"BAD=NAME"}}}`},
		{"both-tls-sources", `{"TLS":{"Certificates":{"a":{"PEM":"x","File":"/x"}}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveProtected([]byte(test.data), &memoryLoader{}, func(string) (string, bool) { return "", false })
			if err == nil {
				t.Fatal("ResolveProtected succeeded")
			}
		})
	}
	large := strings.Repeat("x", MaxImmutableItemBytes+1)
	_, err := ResolveProtected([]byte(`{"Secrets":{"a":{"Env":"TOKEN"}}}`), &memoryLoader{}, func(string) (string, bool) { return large, true })
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized immutable input error = %v", err)
	}
}

func TestOpenSnapshotIsOpaqueBoundedAndOwnsFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.zip")
	payload := []byte("not parsed by bootstrap")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := OpenSnapshot(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(input)
	if closeErr := input.Close(); err != nil || closeErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("snapshot = %q, read=%v, close=%v", got, err, closeErr)
	}
	stdin, err := OpenSnapshot("-", bytes.NewReader([]byte("stdin archive")))
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(stdin)
	if err != nil || string(got) != "stdin archive" {
		t.Fatalf("stdin snapshot = %q, %v", got, err)
	}
	tooLarge, err := OpenSnapshot("-", io.LimitReader(zeroReader{}, api.SnapshotArchiveMaxBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, tooLarge)
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized snapshot error = %v", err)
	}
}

func FuzzCombinedProtectedSplit(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"Protected":{"Secrets":{}},"nodes":{}}`,
		`{"Protected":{},"Protected":{}}`,
		`{"Protected":null}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		regular, protected, found, err := splitProtected([]byte(text))
		if err != nil {
			return
		}
		if _, err := parseUniqueHuJSON(regular); err != nil {
			t.Fatalf("successful split returned invalid regular input: %v", err)
		}
		if found {
			if len(protected) == 0 {
				t.Fatal("successful split returned empty protected value")
			}
		}
	})
}

func FuzzResolveProtectedDoesNotPanic(f *testing.F) {
	f.Add(`{"Secrets":{"a":{"Value":"x"}}}`)
	f.Add(`{"TLS":{"Certificates":{"a":{"File":"/fixed"}}}}`)
	f.Fuzz(func(t *testing.T, text string) {
		loader := &memoryLoader{trusted: map[string][]byte{"/fixed": []byte("pem")}}
		_, _ = ResolveProtected([]byte(text), loader, func(string) (string, bool) { return "value", true })
	})
}
