//go:build windows

package platform

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsServiceLogUsesPrivateProgramDataACL(t *testing.T) {
	programData := t.TempDir()
	oldStatus := windowsServiceStatus
	oldKnownFolder := windowsKnownFolderPath
	windowsServiceStatus = func() (bool, error) { return true, nil }
	windowsKnownFolderPath = func(*windows.KNOWNFOLDERID, uint32) (string, error) { return programData, nil }
	t.Cleanup(func() {
		windowsServiceStatus = oldStatus
		windowsKnownFolderPath = oldKnownFolder
	})

	writer, service, err := OpenServiceLog()
	if err != nil {
		t.Fatal(err)
	}
	if !service || writer == nil {
		t.Fatal("OpenServiceLog did not enable the SCM service sink")
	}
	if _, err = io.WriteString(writer, "private record\n"); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}

	logDirectory := filepath.Join(programData, windowsApplicationDirectory, "logs")
	if err = checkWindowsDirectory(logDirectory, windowsPathPrivateDirectory); err != nil {
		t.Fatalf("service log directory ACL: %v", err)
	}
	path := filepath.Join(logDirectory, windowsServiceLogName)
	handle, err := openWindowsPath(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, false)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	if err = checkWindowsACL(handle, path, windowsPathPrivateFile); err != nil {
		t.Fatalf("service log ACL: %v", err)
	}
}

func TestWindowsInteractiveLoggingKeepsConsoleSink(t *testing.T) {
	oldStatus := windowsServiceStatus
	windowsServiceStatus = func() (bool, error) { return false, nil }
	t.Cleanup(func() { windowsServiceStatus = oldStatus })

	writer, service, err := OpenServiceLog()
	if err != nil {
		t.Fatal(err)
	}
	if service || writer != nil {
		if writer != nil {
			_ = writer.Close()
		}
		t.Fatalf("interactive sink = (%T, %t), want (nil, false)", writer, service)
	}
}

func TestWindowsServiceLogRejectsBroadExistingACL(t *testing.T) {
	programData := t.TempDir()
	applicationDirectory := filepath.Join(programData, windowsApplicationDirectory)
	if err := os.Mkdir(applicationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(applicationDirectory, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}

	oldStatus := windowsServiceStatus
	oldKnownFolder := windowsKnownFolderPath
	windowsServiceStatus = func() (bool, error) { return true, nil }
	windowsKnownFolderPath = func(*windows.KNOWNFOLDERID, uint32) (string, error) { return programData, nil }
	t.Cleanup(func() {
		windowsServiceStatus = oldStatus
		windowsKnownFolderPath = oldKnownFolder
	})
	if writer, _, err := OpenServiceLog(); err == nil {
		if writer != nil {
			_ = writer.Close()
		}
		t.Fatal("OpenServiceLog accepted a broad existing ACL")
	}
}
