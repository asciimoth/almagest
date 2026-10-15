//go:build linux && amd64

package networkrole

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
)

func TestValidateUserAdminEndpointsRequiresPrivateDisjointParent(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime", "writer", "broker", "admin"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	production := LinuxProduction{Profile: "linux-user-v1", Paths: LinuxProductionPaths{
		RuntimeDirectory: filepath.Join(root, "runtime"), WriterDirectory: filepath.Join(root, "writer"),
		BrokerDirectory: filepath.Join(root, "broker"), Launcher: "/usr/bin/true", RuntimeExecutable: "/usr/bin/true",
	}}
	valid := []admingate.EndpointConfig{{Kind: admingate.EndpointUnix, Address: filepath.Join(root, "admin", "admin.sock")}}
	if err := validateUserAdminEndpoints(valid, production); err != nil {
		t.Fatal(err)
	}
	invalid := []admingate.EndpointConfig{{Kind: admingate.EndpointUnix, Address: filepath.Join(root, "runtime", "admin.sock")}}
	if err := validateUserAdminEndpoints(invalid, production); err == nil {
		t.Fatal("role-writable admin directory accepted")
	}
	if err := os.Chmod(filepath.Join(root, "admin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateUserAdminEndpoints(valid, production); err == nil {
		t.Fatal("non-private admin directory accepted")
	}
}

func TestRecoverUserCodeGenerationUsesReceipt(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	broker, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close() //nolint:errcheck
	identity := LinuxIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	generation := ".code-test"
	path := filepath.Join(root, generation)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "role"), []byte("code"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := writeUserCodeReceipt(root, broker, generation, identity); err != nil {
		t.Fatal(err)
	}
	if err := recoverUserCodeGeneration(root, broker, identity); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{generation, ".code-generation"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("recovered object %s remains: %v", name, err)
		}
	}
}

func TestRecoverUserCodeGenerationRejectsOrphan(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".code-orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	broker, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close() //nolint:errcheck
	if err := recoverUserCodeGeneration(root, broker, LinuxIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}); err == nil {
		t.Fatal("orphan generation accepted")
	}
}

func TestRecoverUserCodeGenerationPreservesSubstitutedTreeAndReceipt(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	broker, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close() //nolint:errcheck
	identity := LinuxIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	generation := ".code-test"
	path := filepath.Join(root, generation)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeUserCodeReceipt(root, broker, generation, identity); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, ".saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recoverUserCodeGeneration(root, broker, identity); err == nil {
		t.Fatal("substituted generation accepted")
	}
	for _, name := range []string{generation, ".code-generation"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatalf("fail-closed recovery removed %s: %v", name, err)
		}
	}
}
