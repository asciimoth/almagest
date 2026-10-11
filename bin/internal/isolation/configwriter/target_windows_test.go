//go:build windows

package configwriter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/windows"
)

func windowsPrivateTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;OICI;FA;;;%s)", user.User.Sid.String()))
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	return directory
}

func testConfigPath(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(windowsPrivateTestDirectory(t), "config.hujson")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWindowsTargetReplaceAndUnsafeNames(t *testing.T) {
	directory := windowsPrivateTestDirectory(t)
	path := filepath.Join(directory, "config.hujson")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(testSession(), path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
		Meta: testMeta("replace"), ExpectedRevision: protocol.ConfigRevision([]byte("old")), Data: []byte("new"),
	})
	if err != nil || result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("replace = %+v, %v", result, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("target = %q, %v", data, err)
	}

	for _, unsafe := range []string{
		path + ":stream", `\\server\share\config`, `\\?\C:\config`,
		filepath.Join(directory, "CON"), filepath.Join(directory, "trailing. "),
	} {
		if _, err := New(testSession(), unsafe); err == nil {
			t.Errorf("accepted unsafe path %q", unsafe)
		}
	}
}

func TestWindowsTargetRejectsHardLink(t *testing.T) {
	directory := windowsPrivateTestDirectory(t)
	path := filepath.Join(directory, "config")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(testSession(), path); err == nil {
		t.Fatal("accepted hard-linked target")
	}
}

func TestWindowsTargetRejectsReparsePoint(t *testing.T) {
	root := windowsPrivateTestDirectory(t)
	targetDirectory := windowsPrivateTestDirectory(t)
	target := filepath.Join(targetDirectory, "config")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "junction")
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(filepath.Join(system, "cmd.exe"), "/d", "/c", "mklink", "/J", junction, targetDirectory).CombinedOutput(); err != nil { //nolint:gosec // Fixed native test command and temporary paths.
		t.Fatalf("create test junction: %v: %s", err, output)
	}
	if _, err := New(testSession(), filepath.Join(junction, "config")); err == nil {
		t.Fatal("accepted target below a reparse-point parent")
	}
}

func TestWindowsTargetPinsParentAgainstRedirection(t *testing.T) {
	root := windowsPrivateTestDirectory(t)
	approved := filepath.Join(root, "approved")
	if err := os.Mkdir(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(approved, "config")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(testSession(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() //nolint:errcheck

	// New holds the approved parent without delete sharing. An attacker cannot
	// rename it and put a junction at the pathname used by later replacements.
	if err := os.Rename(approved, filepath.Join(root, "moved")); err == nil {
		t.Fatal("renamed the held fixed-target parent")
	}
	result, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
		Meta: testMeta("pinned-parent"), ExpectedRevision: protocol.ConfigRevision([]byte("old")), Data: []byte("new"),
	})
	if err != nil || result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("replace through pinned parent = %+v, %v", result, err)
	}
}
