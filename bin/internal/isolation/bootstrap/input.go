// Package bootstrap prepares trusted inputs for isolated daemon roles.
//
// It does not import the daemon runtime, nodes, archive handling, databases, or
// native network backends. The launcher reads protected authority once and
// gives the runtime only resolved immutable values and opaque untrusted input.
package bootstrap

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	tailscalehujson "github.com/tailscale/hujson"
)

const (
	MaxProtectedInputBytes = protocol.MaxConfigBytes
	MaxImmutableInputBytes = protocol.MaxConfigBytes
	MaxImmutableItemBytes  = 1 << 20
)

var (
	ErrInvalidInput    = errors.New("invalid bootstrap input")
	ErrInputTooLarge   = errors.New("bootstrap input exceeds its limit")
	ErrUnsafeInput     = errors.New("unsafe trusted bootstrap input")
	ErrAmbiguousInput  = errors.New("ambiguous bootstrap input")
	errDuplicateMember = errors.New("duplicate object member")
)

// ConfigSources identifies the launch-selected configuration sources.
// UnsafeRegular affects only RegularPath. ProtectedPath always uses trusted
// path checks.
type ConfigSources struct {
	RegularPath      string
	ProtectedPath    string
	CombinedStdin    bool
	UnsafeRegular    bool
	DefaultRegular   []byte
	DefaultProtected []byte
	Stdin            io.Reader
}

// ConfigInputs are immutable copies. Runtime config parsing happens after the
// runtime restrictions are installed.
type ConfigInputs struct {
	Regular   []byte
	Protected []byte
}

// FileLoader fixes the file authority used by trusted bootstrap. A regular
// config can use the explicitly unsafe loader. Protected inputs cannot.
type FileLoader interface {
	ReadTrusted(path string, limit int64) ([]byte, error)
	ReadUntrusted(path string, limit int64) ([]byte, error)
}

// LoadConfig reads each selected source once. It performs only the narrow
// top-level split required for the legacy combined stdin contract.
func LoadConfig(sources ConfigSources, loader FileLoader) (ConfigInputs, error) {
	if loader == nil {
		return ConfigInputs{}, fmt.Errorf("%w: file loader is nil", ErrInvalidInput)
	}
	if sources.CombinedStdin {
		if sources.Stdin == nil {
			return ConfigInputs{}, fmt.Errorf("%w: combined stdin is nil", ErrInvalidInput)
		}
		data, err := readBounded(sources.Stdin, MaxProtectedInputBytes)
		if err != nil {
			return ConfigInputs{}, fmt.Errorf("read combined stdin: %w", err)
		}
		if len(data) == 0 {
			data = bytes.Clone(sources.DefaultRegular)
		}
		regular, protected, found, err := splitProtected(data)
		if err != nil {
			return ConfigInputs{}, err
		}
		if !found {
			protected = bytes.Clone(sources.DefaultProtected)
		}
		return ConfigInputs{Regular: regular, Protected: protected}, nil
	}
	if sources.RegularPath == "" || sources.ProtectedPath == "" {
		return ConfigInputs{}, fmt.Errorf("%w: regular and protected paths are required", ErrInvalidInput)
	}
	readRegular := loader.ReadTrusted
	if sources.UnsafeRegular {
		readRegular = loader.ReadUntrusted
	}
	regular, err := readRegular(sources.RegularPath, protocol.MaxConfigBytes)
	if err != nil {
		return ConfigInputs{}, fmt.Errorf("read regular config: %w", err)
	}
	protected, err := loader.ReadTrusted(sources.ProtectedPath, MaxProtectedInputBytes)
	if err != nil {
		return ConfigInputs{}, fmt.Errorf("read protected config: %w", err)
	}
	return ConfigInputs{Regular: regular, Protected: protected}, nil
}

func splitProtected(data []byte) (regular, protected []byte, found bool, err error) {
	value, err := parseUniqueHuJSON(data)
	if err != nil {
		if errors.Is(err, errDuplicateMember) {
			return nil, nil, false, fmt.Errorf("%w: combined stdin: %v", ErrAmbiguousInput, err)
		}
		return nil, nil, false, fmt.Errorf("%w: combined stdin: %v", ErrInvalidInput, err)
	}
	root, ok := value.Value.(*tailscalehujson.Object)
	if !ok {
		return nil, nil, false, fmt.Errorf("%w: combined stdin root is not an object", ErrInvalidInput)
	}
	index := -1
	for i := range root.Members {
		nameValue := root.Members[i].Name.Clone()
		nameValue.Standardize()
		var name string
		if nameErr := json.Unmarshal(nameValue.Pack(), &name); nameErr != nil {
			return nil, nil, false, fmt.Errorf("%w: combined stdin member name: %v", ErrInvalidInput, nameErr)
		}
		if name == "Protected" {
			if index >= 0 {
				return nil, nil, false, fmt.Errorf("%w: duplicate Protected member", ErrAmbiguousInput)
			}
			index = i
		}
	}
	if index < 0 {
		return bytes.Clone(data), nil, false, nil
	}
	protected = root.Members[index].Value.Clone().Pack()
	copy(root.Members[index:], root.Members[index+1:])
	root.Members = root.Members[:len(root.Members)-1]
	return value.Pack(), protected, true, nil
}

func parseUniqueHuJSON(data []byte) (tailscalehujson.Value, error) {
	parsed, err := tailscalehujson.Parse(data)
	if err != nil {
		return tailscalehujson.Value{}, err
	}
	copy := parsed.Clone()
	copy.Standardize()
	if err := rejectDuplicateJSONMembers(copy.Pack()); err != nil {
		return tailscalehujson.Value{}, err
	}
	return parsed, nil
}

func rejectDuplicateJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, tokenErr := decoder.Token()
				if tokenErr != nil {
					return tokenErr
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("%w %q", errDuplicateMember, key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected delimiter %q", delimiter)
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

// Secret is one resolved immutable runtime secret. Source does not reveal the
// environment variable name.
type Secret struct {
	Value  string
	Nodes  []string
	Source string
}

// RuntimeProtected contains only protected values required by R. It has no
// source paths or environment variable names.
type RuntimeProtected struct {
	Secrets      map[string]Secret
	Certificates map[string]string
	Policies     map[string]json.RawMessage
	BuiltinCA    string
}

// ResolvedProtected retains the original protected bytes in trusted bootstrap
// and provides a digest for session binding.
type ResolvedProtected struct {
	Runtime RuntimeProtected
	Digest  [32]byte
}

type protectedDocument struct {
	Secrets map[string]struct {
		Value *string  `json:"Value"`
		Env   string   `json:"Env"`
		Nodes []string `json:"Nodes"`
	} `json:"Secrets"`
	TLS struct {
		Certificates map[string]struct {
			PEM  *string `json:"PEM"`
			File string  `json:"File"`
		} `json:"Certificates"`
		Policies  map[string]json.RawMessage `json:"Policies"`
		BuiltinCA struct {
			PEM  *string `json:"PEM"`
			File string  `json:"File"`
		} `json:"BuiltinCA"`
	} `json:"TLS"`
}

// ResolveProtected resolves the exact environment and file references selected
// by protected config. All files use trusted checks. The returned runtime data
// contains no path or environment selector.
func ResolveProtected(data []byte, loader FileLoader, lookupEnv func(string) (string, bool)) (ResolvedProtected, error) {
	if loader == nil || lookupEnv == nil {
		return ResolvedProtected{}, fmt.Errorf("%w: protected resolver dependency is nil", ErrInvalidInput)
	}
	parsed, err := parseUniqueHuJSON(data)
	if err != nil {
		return ResolvedProtected{}, fmt.Errorf("%w: protected config: %v", ErrInvalidInput, err)
	}
	standard := parsed.Clone()
	standard.Standardize()
	standardBytes := standard.Pack()
	var document protectedDocument
	// The complete protected document has fields owned by other brokers. Decode
	// a raw root first so this resolver only interprets its narrow input set.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(standardBytes, &root); err != nil {
		return ResolvedProtected{}, fmt.Errorf("%w: protected config: %v", ErrInvalidInput, err)
	}
	narrow, err := json.Marshal(map[string]json.RawMessage{"Secrets": root["Secrets"], "TLS": root["TLS"]})
	if err != nil {
		return ResolvedProtected{}, err
	}
	if err := json.Unmarshal(narrow, &document); err != nil {
		return ResolvedProtected{}, fmt.Errorf("%w: protected immutable inputs: %v", ErrInvalidInput, err)
	}

	runtime := RuntimeProtected{}
	total := 0
	if len(document.Secrets) > 0 {
		runtime.Secrets = make(map[string]Secret, len(document.Secrets))
	}
	for name, input := range document.Secrets {
		if input.Value != nil == (strings.TrimSpace(input.Env) != "") {
			return ResolvedProtected{}, fmt.Errorf("%w: secret %q must set exactly one of Value or Env", ErrInvalidInput, name)
		}
		value, source := "", "protected"
		if input.Value != nil {
			value = *input.Value
		} else {
			if !validEnvironmentName(input.Env) {
				return ResolvedProtected{}, fmt.Errorf("%w: secret %q has invalid environment name", ErrInvalidInput, name)
			}
			var ok bool
			value, ok = lookupEnv(input.Env)
			if !ok {
				return ResolvedProtected{}, fmt.Errorf("%w: environment value for secret %q is not set", ErrInvalidInput, name)
			}
			source = "environment"
		}
		if err := addImmutableSize(&total, len(value)); err != nil {
			return ResolvedProtected{}, fmt.Errorf("secret %q: %w", name, err)
		}
		runtime.Secrets[name] = Secret{Value: value, Nodes: append([]string(nil), input.Nodes...), Source: source}
	}
	if len(document.TLS.Certificates) > 0 {
		runtime.Certificates = make(map[string]string, len(document.TLS.Certificates))
	}
	for name, input := range document.TLS.Certificates {
		pem, err := resolvePEM(name, input.PEM, input.File, loader)
		if err != nil {
			return ResolvedProtected{}, err
		}
		if err := addImmutableSize(&total, len(pem)); err != nil {
			return ResolvedProtected{}, fmt.Errorf("TLS certificate %q: %w", name, err)
		}
		runtime.Certificates[name] = pem
	}
	if len(document.TLS.Policies) > 0 {
		runtime.Policies = make(map[string]json.RawMessage, len(document.TLS.Policies))
		for name, policy := range document.TLS.Policies {
			runtime.Policies[name] = bytes.Clone(policy)
		}
	}
	if document.TLS.BuiltinCA.PEM != nil || strings.TrimSpace(document.TLS.BuiltinCA.File) != "" {
		pem, err := resolvePEM("built-in CA", document.TLS.BuiltinCA.PEM, document.TLS.BuiltinCA.File, loader)
		if err != nil {
			return ResolvedProtected{}, err
		}
		if err := addImmutableSize(&total, len(pem)); err != nil {
			return ResolvedProtected{}, fmt.Errorf("built-in CA: %w", err)
		}
		runtime.BuiltinCA = pem
	}
	return ResolvedProtected{Runtime: runtime, Digest: sha256.Sum256(standardBytes)}, nil
}

func resolvePEM(name string, inline *string, path string, loader FileLoader) (string, error) {
	path = strings.TrimSpace(path)
	if inline != nil == (path != "") {
		return "", fmt.Errorf("%w: TLS input %q must set exactly one of PEM or File", ErrInvalidInput, name)
	}
	if inline != nil {
		return *inline, nil
	}
	data, err := loader.ReadTrusted(path, MaxImmutableItemBytes)
	if err != nil {
		return "", fmt.Errorf("read trusted TLS input %q: %w", name, err)
	}
	return string(data), nil
}

func addImmutableSize(total *int, size int) error {
	if size > MaxImmutableItemBytes || *total > MaxImmutableInputBytes-size {
		return ErrInputTooLarge
	}
	*total += size
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" || len(name) > 256 || !utf8.ValidString(name) {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil || limit < 0 {
		return nil, ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrInputTooLarge
	}
	return data, nil
}

// NativeLoader reads launch-selected files without environment or current
// directory lookup. Its OS-specific trusted implementation rejects replaceable
// path components and unsafe ownership/ACLs.
type NativeLoader struct{}

func (NativeLoader) ReadTrusted(path string, limit int64) ([]byte, error) {
	file, err := openTrustedFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	return readBounded(file, limit)
}

func (NativeLoader) ReadUntrusted(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: input is not a regular file", ErrInvalidInput)
	}
	return readBounded(file, limit)
}

// OpenSnapshot opens untrusted startup state as opaque bounded data. It does
// not parse an archive and does not expose another path-selection operation.
func OpenSnapshot(path string, stdin io.Reader) (io.ReadCloser, error) {
	if path == "-" {
		if stdin == nil {
			return nil, fmt.Errorf("%w: snapshot stdin is nil", ErrInvalidInput)
		}
		return &boundedReadCloser{reader: io.LimitReader(stdin, api.SnapshotArchiveMaxBytes+1), remaining: api.SnapshotArchiveMaxBytes}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open startup snapshot: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > api.SnapshotArchiveMaxBytes {
		_ = file.Close()
		return nil, fmt.Errorf("%w: snapshot must be a regular file of at most %d bytes", ErrInvalidInput, api.SnapshotArchiveMaxBytes)
	}
	return &boundedReadCloser{
		reader: io.LimitReader(file, api.SnapshotArchiveMaxBytes+1), remaining: api.SnapshotArchiveMaxBytes, closer: file,
	}, nil
}

type boundedReadCloser struct {
	reader    io.Reader
	remaining int64
	overflow  bool
	closer    io.Closer
}

func (r *boundedReadCloser) Read(buffer []byte) (int, error) {
	if r.overflow {
		return 0, ErrInputTooLarge
	}
	if int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	n, err := r.reader.Read(buffer)
	if int64(n) > r.remaining {
		r.overflow = true
		return 0, ErrInputTooLarge
	}
	r.remaining -= int64(n)
	return n, err
}

func (r *boundedReadCloser) Close() error {
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}
