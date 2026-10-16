//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"github.com/asciimoth/almagest/lib/sandbox"
	"golang.org/x/sys/windows"
)

func TestWindowsDirectDaemonBuildsNativeSandboxService(t *testing.T) {
	if windows.RtlGetVersion().BuildNumber != 20348 {
		t.Skip("windows-native-v1 is qualified on Windows Server 2022 build 20348")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	conf := &Config{}
	conf.Protected.Sandbox = policy.Config{
		Mode: sandbox.Strict, TemporaryRoot: filepath.Join(root, "temporary"), RegistryRoot: filepath.Join(root, "registry"),
		Programs: map[sandbox.ProgramID]policy.Program{"helper": {Executable: executable, Builder: "fixed-v1"}},
		Templates: map[sandbox.TemplateID]policy.Template{"test": {AllowedClasses: []string{"test/Node"}, Programs: []sandbox.ProgramID{"helper"},
			RuntimeProfile: "windows-native-v1", Limits: policy.TemplateLimits{Tasks: 4, MemoryBytes: 256 << 20, CPUPercent: 100}}},
	}
	service, err := directDaemonIntegrations().buildSandboxes(conf)
	if err != nil {
		t.Fatal(err)
	}
	if service == nil {
		t.Fatal("direct daemon did not construct the configured sandbox service")
	}
	t.Cleanup(func() {
		if closeErr := service.Close(); closeErr != nil {
			t.Errorf("close service: %v", closeErr)
		}
	})
	reporter, ok := service.(sandbox.StatusReporter)
	if !ok {
		t.Fatal("daemon sandbox service has no status reporter")
	}
	status := reporter.ServiceStatus().Templates["test"]
	if status.Profile != "windows-native-v1" || !status.StrictSatisfied || status.FullBoundary {
		t.Fatalf("native Windows daemon sandbox status = %+v", status)
	}
}
