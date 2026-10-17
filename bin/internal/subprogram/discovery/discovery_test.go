package discovery

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

func TestConfigureDisabledDoesNotSearch(t *testing.T) {
	called := false
	input := policy.Config{}
	got, found, err := Configure(input, Options{LookPath: func(string) (string, error) {
		called = true
		return "", errors.New("unexpected lookup")
	}})
	if err != nil || called || len(found) != 0 || got.DiscoverSystemPrograms {
		t.Fatalf("Configure() = (%+v, %+v, %v), lookup=%v", got, found, err, called)
	}
}

func TestConfigureBuildsTorAndTransportPolicy(t *testing.T) {
	root := t.TempDir()
	torPath := testExecutable(t, root, "tor")
	obfsPath := testExecutable(t, root, "lyrebird")
	dependency := filepath.Join(root, "libtor.so")
	if err := os.WriteFile(dependency, []byte("library"), 0o444); err != nil {
		t.Fatal(err)
	}
	lookups := map[string]string{"tor": torPath, "lyrebird": obfsPath}
	configured, found, err := Configure(policy.Config{DiscoverSystemPrograms: true}, Options{
		StateRoot: filepath.Join(root, "state"), GOOS: "linux", LinuxLauncher: filepath.Join(root, "launcher"),
		LinuxIdentity: policy.Linux{UID: 123, GID: 456},
		LookPath: func(name string) (string, error) {
			if path := lookups[name]; path != "" {
				return path, nil
			}
			return "", exec.ErrNotFound
		},
		RuntimeInputs: func(paths ...string) ([]policy.ReadOnly, error) {
			if len(paths) != 1 || paths[0] != torPath && paths[0] != obfsPath {
				t.Fatalf("runtime input paths = %q", paths)
			}
			return []policy.ReadOnly{{Source: dependency, Target: "/lib/" + filepath.Base(paths[0]) + ".so", Executable: true}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Program != "tor" || found[1].Program != "obfs4" {
		t.Fatalf("found = %+v", found)
	}
	tor := configured.Programs["tor"]
	if tor.Executable != torPath || !slices.Equal(tor.Descendants, []sandbox.ProgramID{"obfs4"}) || len(tor.Arguments) != 4 {
		t.Fatalf("Tor program = %+v", tor)
	}
	template := configured.Templates["tor"]
	if template.RuntimeProfile != "linux-landlock-v1" || !slices.Equal(template.Programs, []sandbox.ProgramID{"tor", "obfs4"}) ||
		template.Linux.UID != 123 || len(template.ReadOnly) != 2 {
		t.Fatalf("Tor template = %+v", template)
	}
	if _, err = policy.Normalize(configured); err != nil {
		t.Fatalf("generated policy is invalid: %v", err)
	}
}

func TestConfigureExplicitDefinitionsTakePrecedence(t *testing.T) {
	root := t.TempDir()
	explicit := testExecutable(t, root, "explicit-tor")
	input := policy.Config{
		DiscoverSystemPrograms: true,
		Programs:               map[sandbox.ProgramID]policy.Program{"tor": {Executable: explicit, Builder: "fixed-v1"}},
		Templates: map[sandbox.TemplateID]policy.Template{"tor": {
			AllowedClasses: []string{"custom/Node"}, Programs: []sandbox.ProgramID{"tor"}, RuntimeProfile: "direct-v1",
		}},
	}
	configured, found, err := Configure(input, Options{
		StateRoot: filepath.Join(root, "state"), GOOS: runtime.GOOS,
		LookPath: func(string) (string, error) { return "", errors.New("must not search") },
	})
	if err != nil || len(found) != 0 {
		t.Fatalf("Configure() found=%+v err=%v", found, err)
	}
	if configured.Programs["tor"].Executable != explicit || configured.Templates["tor"].AllowedClasses[0] != "custom/Node" {
		t.Fatalf("explicit policy changed: %+v", configured)
	}
}

func TestConfigureMissingTorDefersFailureToTemplateUse(t *testing.T) {
	configured, found, err := Configure(policy.Config{DiscoverSystemPrograms: true}, Options{
		StateRoot: t.TempDir(), LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	})
	if err != nil || len(found) != 0 || len(configured.Programs) != 0 || len(configured.Templates) != 0 || configured.UnavailableTemplates["tor"] == "" {
		t.Fatalf("Configure() = (%+v, %+v, %v)", configured, found, err)
	}
	if _, err = policy.Normalize(configured); err != nil {
		t.Fatalf("deferred failure policy is invalid: %v", err)
	}
}

func TestConfigureBrokenOptionalTransportKeepsTorAvailable(t *testing.T) {
	root := t.TempDir()
	torPath := testExecutable(t, root, "tor")
	obfsPath := testExecutable(t, root, "lyrebird")
	configured, found, err := Configure(policy.Config{DiscoverSystemPrograms: true}, Options{
		StateRoot: filepath.Join(root, "state"), GOOS: "linux", LinuxLauncher: filepath.Join(root, "launcher"),
		LookPath: func(name string) (string, error) {
			if name == "tor" {
				return torPath, nil
			}
			return obfsPath, nil
		},
		RuntimeInputs: func(paths ...string) ([]policy.ReadOnly, error) {
			if paths[0] == obfsPath {
				return nil, errors.New("missing transport library")
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Program != "tor" || configured.Programs["tor"].Executable != torPath {
		t.Fatalf("Tor discovery result = found=%+v config=%+v", found, configured)
	}
	if configured.UnavailableTemplates["tor"] != "" || configured.UnavailablePrograms["obfs4"] == "" {
		t.Fatalf("discovery diagnostics = templates=%+v programs=%+v", configured.UnavailableTemplates, configured.UnavailablePrograms)
	}
	if !slices.Equal(configured.Templates["tor"].Programs, []sandbox.ProgramID{"tor"}) {
		t.Fatalf("Tor template programs = %q", configured.Templates["tor"].Programs)
	}
	if _, err = policy.Normalize(configured); err != nil {
		t.Fatalf("generated policy is invalid: %v", err)
	}
}

func TestConfigureMissingGeneratedLauncherDefersFailure(t *testing.T) {
	root := t.TempDir()
	torPath := testExecutable(t, root, "tor")
	configured, found, err := Configure(policy.Config{DiscoverSystemPrograms: true}, Options{
		StateRoot: filepath.Join(root, "state"), GOOS: "linux",
		LookPath: func(name string) (string, error) {
			if name == "tor" {
				return torPath, nil
			}
			return "", exec.ErrNotFound
		},
		RuntimeInputs: func(...string) ([]policy.ReadOnly, error) { return nil, nil },
	})
	if err != nil || len(found) != 0 || len(configured.Programs) != 0 || len(configured.Templates) != 0 || configured.UnavailableTemplates["tor"] == "" {
		t.Fatalf("Configure() = (%+v, %+v, %v)", configured, found, err)
	}
	if _, err = policy.Normalize(configured); err != nil {
		t.Fatalf("deferred launcher failure policy is invalid: %v", err)
	}
}

func testExecutable(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("test"), 0o555); err != nil {
		t.Fatal(err)
	}
	return path
}
