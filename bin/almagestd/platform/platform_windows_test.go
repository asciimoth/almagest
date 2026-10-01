//go:build windows

package platform

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	almargs "github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWindowsDefaultConfigUsesProgramData(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "Machine Data ü")
	useWindowsKnownFolders(t, programData, filepath.Join(t.TempDir(), "User Data"))

	agent, err := newWindowsAgent(almargs.Args{})
	if err != nil {
		t.Fatalf("newWindowsAgent: %v", err)
	}
	wantConfig := filepath.Join(programData, windowsApplicationDirectory, "alm.hujson")
	wantProtected := filepath.Join(programData, windowsApplicationDirectory, "protected_alm.hujson")
	if agent.ConfigPath != wantConfig || agent.ProtectedPath != wantProtected {
		t.Fatalf("config paths = %q, %q; want %q, %q", agent.ConfigPath, agent.ProtectedPath, wantConfig, wantProtected)
	}
	mainData, protectedData, err := agent.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if string(mainData) != DefaultConfigText || string(protectedData) != DefaultProtectedConfigText {
		t.Fatal("new default config files do not contain the embedded Windows defaults")
	}
}

func TestWindowsConfigDirectoryOverridesAndWrites(t *testing.T) {
	base := filepath.Join(t.TempDir(), "配置 with spaces")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	protectedPath := filepath.Join(t.TempDir(), "separate protected", "host.hujson")
	agent, err := newWindowsAgent(almargs.Args{Config: base, ProtectedConfig: protectedPath})
	if err != nil {
		t.Fatalf("newWindowsAgent: %v", err)
	}
	if want := filepath.Join(base, "alm.hujson"); agent.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", agent.ConfigPath, want)
	}
	if agent.ProtectedPath != protectedPath {
		t.Fatalf("ProtectedPath = %q, want %q", agent.ProtectedPath, protectedPath)
	}
	updated := []byte("{\n  \"nodes\": {}\n}\n")
	if err := agent.WriteConfig(updated); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	got, err := os.ReadFile(agent.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, updated) {
		t.Fatalf("written config = %q, want %q", got, updated)
	}
	protected, err := os.ReadFile(protectedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(protected) != DefaultProtectedConfigText {
		t.Fatal("writing regular config changed the separate protected config")
	}
}

func TestWindowsConfigDurableWriteFailureAndConcurrency(t *testing.T) {
	base := filepath.Join(t.TempDir(), "config")
	agent, err := newWindowsAgent(almargs.Args{Config: base})
	if err != nil {
		t.Fatalf("newWindowsAgent: %v", err)
	}
	original, err := os.ReadFile(agent.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(agent.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.WriteConfig([]byte(`{"locked":true}`)); err == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("WriteConfig succeeded while the destination denied delete sharing")
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(agent.ConfigPath); err != nil || !bytes.Equal(data, original) {
		t.Fatalf("config after failed replacement = %q, err=%v", data, err)
	}

	payloads := make([][]byte, 16)
	var wait sync.WaitGroup
	errorsByWrite := make(chan error, len(payloads))
	for i := range payloads {
		payloads[i] = fmt.Appendf(nil, `{"save":%d}`, i)
		wait.Add(1)
		go func(data []byte) {
			defer wait.Done()
			errorsByWrite <- agent.WriteConfig(data)
		}(payloads[i])
	}
	wait.Wait()
	close(errorsByWrite)
	for err := range errorsByWrite {
		if err != nil {
			t.Fatalf("concurrent WriteConfig: %v", err)
		}
	}
	written, err := readPrivateWindowsFile(agent.ConfigPath)
	if err != nil {
		t.Fatalf("read final private config: %v", err)
	}
	for _, payload := range payloads {
		if bytes.Equal(written, payload) {
			return
		}
	}
	t.Fatalf("final config is a partial or unknown write: %q", written)
}

func TestWindowsRelativeConfigPathsResolveBeforeChdir(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	agent, err := newWindowsAgent(almargs.Args{
		Config:          filepath.Join("config relative", "alm.hujson"),
		ProtectedConfig: filepath.Join("protected relative", "host.hujson"),
	})
	if err != nil {
		t.Fatalf("newWindowsAgent: %v", err)
	}
	if !filepath.IsAbs(agent.ConfigPath) || !filepath.IsAbs(agent.ProtectedPath) {
		t.Fatalf("config paths are not absolute: %q, %q", agent.ConfigPath, agent.ProtectedPath)
	}
	originalConfigPath := agent.ConfigPath
	other := t.TempDir()
	t.Chdir(other)
	if _, _, err := agent.ReadConfig(); err != nil {
		t.Fatalf("ReadConfig after chdir: %v", err)
	}
	if agent.ConfigPath != originalConfigPath {
		t.Fatalf("ConfigPath changed after chdir: %q", agent.ConfigPath)
	}
}

func TestWindowsStdinConfigIsReadOnlyAndSplitsProtected(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "stdin-*.hujson")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString(`{"nodes":{},"Protected":{"System":{"Type":"debug"}}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = input.Close()
	})

	agent, err := newWindowsAgent(almargs.Args{StdInConfig: true})
	if err != nil {
		t.Fatalf("newWindowsAgent: %v", err)
	}
	mainData, protectedData, err := agent.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if strings.Contains(string(mainData), "Protected") || !strings.Contains(string(protectedData), `"Type":"debug"`) {
		t.Fatalf("stdin split returned main=%s protected=%s", mainData, protectedData)
	}
	if !agent.ConfRO() {
		t.Fatal("stdin config is not read-only")
	}
	if err := agent.WriteConfig([]byte("{}")); !errors.Is(err, ErrConfigReadOnly) {
		t.Fatalf("WriteConfig error = %v, want ErrConfigReadOnly", err)
	}
}

func TestWindowsSetupFSSelectsServiceAndUserKnownFolders(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "Program Data")
	localData := filepath.Join(t.TempDir(), "Local Data")
	useWindowsKnownFolders(t, programData, localData)

	for _, test := range []struct {
		name       string
		service    bool
		wantParent string
	}{
		{name: "service", service: true, wantParent: programData},
		{name: "interactive user", service: false, wantParent: localData},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldStatus := windowsServiceStatus
			windowsServiceStatus = func() (bool, error) { return test.service, nil }
			t.Cleanup(func() { windowsServiceStatus = oldStatus })
			storage, cleanup, err := SetupFS("", false)
			if err != nil {
				t.Fatalf("SetupFS: %v", err)
			}
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
			want := filepath.Join(test.wantParent, windowsApplicationDirectory, "store")
			if storage != want {
				t.Fatalf("storage = %q, want %q", storage, want)
			}
		})
	}
}

func TestWindowsTemporaryStorageIsIdentityLocalAndRemoved(t *testing.T) {
	localData := filepath.Join(t.TempDir(), "Local Data ü")
	useWindowsKnownFolders(t, filepath.Join(t.TempDir(), "Program Data"), localData)
	ignored := filepath.Join(t.TempDir(), "ignored")
	storage, cleanup, err := SetupFS(ignored, true)
	if err != nil {
		t.Fatalf("SetupFS: %v", err)
	}
	wantParent := filepath.Join(localData, windowsApplicationDirectory, "tmp")
	if filepath.Dir(storage) != wantParent || !strings.HasPrefix(filepath.Base(storage), "alm-store-") {
		t.Fatalf("temporary storage = %q, want generated child of %q", storage, wantParent)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(storage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary storage still exists after cleanup: %v", err)
	}
}

func TestWindowsExplicitStorageOverridesKnownFolders(t *testing.T) {
	oldKnownFolder := windowsKnownFolderPath
	oldServiceStatus := windowsServiceStatus
	windowsKnownFolderPath = func(*windows.KNOWNFOLDERID, uint32) (string, error) {
		return "", errors.New("known folder must not be queried")
	}
	windowsServiceStatus = func() (bool, error) {
		return false, errors.New("service state must not be queried")
	}
	t.Cleanup(func() {
		windowsKnownFolderPath = oldKnownFolder
		windowsServiceStatus = oldServiceStatus
	})

	want := filepath.Join(t.TempDir(), "explicit store")
	storage, cleanup, err := SetupFS(want, false)
	if err != nil {
		t.Fatalf("SetupFS: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if storage != want {
		t.Fatalf("storage = %q, want %q", storage, want)
	}
}

func TestWindowsPrivateStateRejectsUnsafeDACLs(t *testing.T) {
	for _, test := range []struct {
		name     string
		sddl     string
		nullDACL bool
	}{
		{name: "unrelated full access", sddl: "D:P(A;;GA;;;WD)"},
		{name: "null DACL", nullDACL: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(windowsPrivateTestTempDir(t), "private.hujson")
			if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.nullDACL {
				setWindowsTestNullDACL(t, path)
			} else {
				setWindowsTestDACL(t, path, test.sddl)
			}
			if _, err := readPrivateWindowsFile(path); err == nil {
				t.Fatal("readPrivateWindowsFile accepted an unsafe DACL")
			}
		})
	}
}

func TestWindowsStorageACLProtectsGeneratedState(t *testing.T) {
	storage := filepath.Join(windowsPrivateTestTempDir(t), "private-store")
	storage, cleanup, err := SetupFS(storage, false)
	if err != nil {
		t.Fatalf("SetupFS: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	directory := filepath.Join(storage, "generated-secrets")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkWindowsDirectory(directory, windowsPathPrivateDirectory); err != nil {
		t.Fatalf("generated directory did not inherit the private ACL: %v", err)
	}
	secret := filepath.Join(directory, "secret.bin")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateWindowsFile(secret); err != nil {
		t.Fatalf("generated file did not inherit the private ACL: %v", err)
	}
}

func TestWindowsPrivateStateRejectsWritableParentAndHardLink(t *testing.T) {
	t.Run("writable parent", func(t *testing.T) {
		parent := filepath.Join(windowsPrivateTestTempDir(t), "replaceable")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		setWindowsTestDACL(t, parent, "D:P(A;;GA;;;WD)")
		if err := createPrivateWindowsFile(filepath.Join(parent, "config.hujson"), []byte("{}")); err == nil {
			t.Fatal("createPrivateWindowsFile accepted an untrusted writable parent")
		}
	})

	t.Run("hard link", func(t *testing.T) {
		path := filepath.Join(windowsPrivateTestTempDir(t), "private.hujson")
		if err := createPrivateWindowsFile(path, []byte("secret")); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, filepath.Join(filepath.Dir(path), "alias.hujson")); err != nil {
			t.Fatal(err)
		}
		if _, err := readPrivateWindowsFile(path); err == nil {
			t.Fatal("readPrivateWindowsFile accepted a hard-linked private file")
		}
	})
}

func TestWindowsPrivateStateRejectsReparsePointsAndStreams(t *testing.T) {
	if _, err := validateWindowsPath(`C:\private\config.hujson:secret`); err == nil {
		t.Fatal("validateWindowsPath accepted an alternate data stream")
	}
	if _, err := validateWindowsPath(`C:\private\..\config.hujson`); err == nil {
		t.Fatal("validateWindowsPath accepted parent traversal")
	}
	if _, err := validateWindowsPath(`\\server\share\config.hujson`); err == nil {
		t.Fatal("validateWindowsPath accepted a UNC path")
	}

	target := filepath.Join(windowsPrivateTestTempDir(t), "target.hujson")
	if err := createPrivateWindowsFile(target, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(target), "link.hujson")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("create Windows symlink: %v", err)
	}
	if _, err := readPrivateWindowsFile(link); err == nil {
		t.Fatal("readPrivateWindowsFile followed a reparse point")
	}
}

func TestWindowsUnsafeConfigDoesNotBypassStorageACL(t *testing.T) {
	configDir := windowsPrivateTestTempDir(t)
	configPath := filepath.Join(configDir, "unsafe.hujson")
	protectedPath := filepath.Join(configDir, "unsafe-protected.hujson")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protectedPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	setWindowsTestDACL(t, configPath, "D:P(A;;GA;;;WD)")
	agent, err := newWindowsAgent(almargs.Args{
		Config: configPath, ProtectedConfig: protectedPath, UnsafeConfig: true,
	})
	if err != nil {
		t.Fatalf("newWindowsAgent with --unsafe-config: %v", err)
	}
	if _, _, err := agent.ReadConfig(); err != nil {
		t.Fatalf("unsafe ReadConfig: %v", err)
	}

	storage := filepath.Join(windowsPrivateTestTempDir(t), "unsafe-store")
	if err := os.Mkdir(storage, 0o700); err != nil {
		t.Fatal(err)
	}
	setWindowsTestDACL(t, storage, "D:P(A;;GA;;;WD)")
	if _, _, err := SetupFS(storage, false); err == nil {
		t.Fatal("SetupFS accepted unsafe storage because config was unsafe")
	}
}

func windowsPrivateTestTempDir(t *testing.T) string {
	t.Helper()
	user, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("get current Windows identity: %v", err)
	}
	if !user.IsWellKnown(windows.WinLocalSystemSid) {
		return t.TempDir()
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatalf("resolve ProgramData for test temporary directory: %v", err)
	}
	path, err := os.MkdirTemp(programData, "almagest-test-")
	if err != nil {
		t.Fatalf("reserve test temporary directory: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove test temporary directory reservation: %v", err)
	}
	if err := makeSurePrivateWindowsDirExists(path); err != nil {
		t.Fatalf("create private test temporary directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove private test temporary directory: %v", err)
		}
	})
	return path
}

func setWindowsTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil && !errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
		t.Fatalf("DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}
}

func setWindowsTestNullDACL(t *testing.T, path string) {
	t.Helper()
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, nil, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo null DACL: %v", err)
	}
}

func TestWindowsRejectsUnixAdminFlags(t *testing.T) {
	socket := `C:\ignored.sock`
	cases := []almargs.Args{
		{UnixAdmin: &socket},
		{TmpUnixAdmin: true},
		{UnixAdminFilterUIDs: []uint32{1000}},
		{UnixAdminFilterGIDs: []uint32{1000}},
		{UnixAdminFilterUsernames: []string{"alice"}},
	}
	for _, arguments := range cases {
		if err := ValidateArgs(&arguments); err == nil {
			t.Fatalf("ValidateArgs(%+v) succeeded", arguments)
		}
	}
	if err := ValidateArgs(&almargs.Args{}); err != nil {
		t.Fatalf("ValidateArgs(empty): %v", err)
	}
}

func TestWindowsRejectsWebAdminIDFilters(t *testing.T) {
	for _, arguments := range []almargs.Args{
		{WebAdminFilterUIDs: []uint32{1000}},
		{WebAdminFilterGIDs: []uint32{1000}},
	} {
		if err := ValidateArgs(&arguments); err == nil {
			t.Fatalf("ValidateArgs(%+v) succeeded", arguments)
		}
	}
	if err := ValidateArgs(&almargs.Args{WebAdminFilterUsernames: []string{"alice"}}); err != nil {
		t.Fatalf("ValidateArgs(username filter): %v", err)
	}
}

func useWindowsKnownFolders(t *testing.T, programData, localData string) {
	t.Helper()
	old := windowsKnownFolderPath
	windowsKnownFolderPath = func(id *windows.KNOWNFOLDERID, _ uint32) (string, error) {
		switch id {
		case windows.FOLDERID_ProgramData:
			return programData, nil
		case windows.FOLDERID_LocalAppData:
			return localData, nil
		default:
			return "", fmt.Errorf("unexpected known-folder identifier")
		}
	}
	t.Cleanup(func() { windowsKnownFolderPath = old })
}

func TestWindowsCapabilityCatalogPreservesUnsupportedPolicies(t *testing.T) {
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	report := system.Capabilities()
	unsupportedOperations := []sysnet.OperationKey{
		{Target: sysnet.TargetTun, Operation: sysnet.OpRename, Family: sysnet.FamilyNone},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpRename, Family: sysnet.FamilyNone},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpSourceRoutes, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetOutNet, Operation: sysnet.OpMulticastUDP, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetOutNet, Operation: sysnet.OpMulticastUDP, Family: sysnet.FamilyIPv6},
	}
	for _, key := range unsupportedOperations {
		if capability := report.Operation(key); capability.State != sysnet.CapabilityUnsupported {
			t.Errorf("operation %+v capability = %+v, want unsupported", key, capability)
		}
	}
	for _, key := range []sysnet.RoutingProfileKey{
		{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingInclude},
		{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingFull, Strict: true},
		{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingExclude, Strict: true},
	} {
		if capability := report.DefaultTunProfile(key).Capability; capability.State != sysnet.CapabilityUnsupported {
			t.Errorf("routing profile %+v capability = %+v, want unsupported", key, capability)
		}
	}

	unsupportedOptions := []sysnet.DefaultTunOpts{
		{Strict: true},
		{Include: []sysnet.Rule{{Type: "win-exe-tree", Rule: `C:\Program Files\Almagest\app.exe`}}},
		{SourceRoutes: []sysnet.TunSourceRoute{{
			Destination: netip.MustParsePrefix("0.0.0.0/0"),
			Source:      netip.MustParseAddr("198.18.0.1"),
		}}},
	}
	for _, opts := range unsupportedOptions {
		if err := system.CheckDefaultTunOpts(opts).Err(); !errors.Is(err, sysnet.ErrNotSupported) {
			t.Errorf("CheckDefaultTunOpts(%+v) error = %v, want ErrNotSupported", opts, err)
		}
	}

	exclude := sysnet.RoutingProfileKey{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingExclude}
	if err := system.CheckRule(
		sysnet.Rule{Type: "uid", Rule: "1000"},
		sysnet.RuleContext{Routing: &exclude},
	).Err(); !errors.Is(err, sysnet.ErrNotSupported) {
		t.Fatalf("Linux rule validation error = %v, want ErrNotSupported", err)
	}
}

func TestWindowsSplitCapabilityRemainsUnknownUntilExclusionAttempt(t *testing.T) {
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	profile := system.Capabilities().DefaultTunProfile(sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4,
		Mode:   sysnet.RoutingExclude,
	})
	for _, binding := range profile.Rules {
		if binding.Type != "win-exe-tree" {
			continue
		}
		if binding.Capability.State != sysnet.CapabilityUnknown {
			t.Fatalf("win-exe-tree binding capability = %+v, want unknown", binding.Capability)
		}
		if !hasCapabilityReason(binding.Capability, sysnet.ReasonProbeNotRun) {
			t.Fatalf("win-exe-tree binding reasons = %v, want probe_not_run", binding.Capability.Reasons)
		}
		return
	}
	t.Fatal("win-exe-tree binding is absent from the exclude profile")
}

func TestWindowsDisabledMatchersFailBeforeBuild(t *testing.T) {
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{
		Features: sysnetwindows.FeatureConfig{DisableMatchers: true},
	})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	matcher := sysnet.MatcherProfileKey{Family: sysnet.FamilyIPv4, Transport: sysnet.TransportTCP}
	rule := sysnet.Rule{Type: "win-pid", Rule: "42"}
	if err := system.CheckRule(rule, sysnet.RuleContext{Matcher: &matcher}).Err(); !errors.Is(err, sysnet.ErrNotSupported) {
		t.Fatalf("CheckRule error = %v, want ErrNotSupported", err)
	}
	if built, err := system.BuildMatcher(rule); built != nil || !errors.Is(err, sysnet.ErrNotSupported) {
		t.Fatalf("BuildMatcher = %T, %v; want nil, ErrNotSupported", built, err)
	}
}

func hasCapabilityReason(capability sysnet.Capability, reason sysnet.CapabilityReason) bool {
	for _, candidate := range capability.Reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

func TestWindowsNativeSystemSelection(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{"Windows":{"Features":{"DisableIPv6":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	system, err := SystemIntegration(cfg, nil)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	if _, ok := system.(*sysnetwindows.System); !ok {
		t.Fatalf("SystemIntegration returned %T, want *windows.System", system)
	}
	capability := system.Capabilities().Operation(sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyIPv6,
	})
	if capability.State != sysnet.CapabilityUnsupported {
		t.Fatalf("disabled IPv6 TUN capability = %#v, want unsupported", capability)
	}
	if err := system.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestWindowsStableGUIDRejectsConcurrentTunRequests(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native Wintun mutation requires an elevated test identity")
	}
	guid, err := windows.GenerateGUID()
	if err != nil {
		t.Fatalf("GenerateGUID: %v", err)
	}
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{
		AdapterNamePrefix: "alm-test",
		StableGUID:        guid.String(),
	})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	first, err := system.BuildTun(sysnet.TunOpts{
		TunAddrs: []string{"198.18.246.1/32"},
	})
	if err != nil {
		t.Fatalf("first BuildTun: %v", err)
	}
	if second, err := system.BuildTun(sysnet.TunOpts{
		TunAddrs: []string{"198.18.247.1/32"},
	}); err == nil {
		if second != nil {
			_ = second.Close()
		}
		t.Fatal("second BuildTun succeeded with the same live stable GUID")
	}
	if _, err := system.GetTunAddrs(first); err != nil {
		t.Fatalf("first TUN is not usable after the collision: %v", err)
	}
}

func TestWindowsOrdinaryTunsDoNotStartSplitDriver(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native TUN mutation requires an elevated test identity")
	}
	assertSplitDriverStopped := func(phase string) {
		t.Helper()
		manager, err := mgr.Connect()
		if err != nil {
			t.Fatalf("%s: connect service manager: %v", phase, err)
		}
		defer manager.Disconnect()
		service, err := manager.OpenService("mullvad-split-tunnel")
		if err != nil {
			t.Fatalf("%s: open split-driver service: %v", phase, err)
		}
		defer service.Close()
		status, err := service.Query()
		if err != nil {
			t.Fatalf("%s: query split-driver service: %v", phase, err)
		}
		if status.State != svc.Stopped {
			t.Fatalf("%s: split-driver state = %v, want stopped", phase, status.State)
		}
	}
	assertSplitDriverStopped("before construction")
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{AdapterNamePrefix: "alm-nosplit"})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	assertSplitDriverStopped("after construction")
	device, err := system.BuildTun(sysnet.TunOpts{TunAddrs: []string{"198.18.245.1/32"}})
	if err != nil {
		t.Fatalf("BuildTun without exclusions: %v", err)
	}
	if err := device.Close(); err != nil {
		t.Fatalf("close regular TUN: %v", err)
	}
	assertSplitDriverStopped("after regular TUN")
	defaultDevice, err := system.BuildDefaultTun(sysnet.DefaultTunOpts{TunAddrs: []string{"198.18.243.1/32"}})
	if err != nil {
		t.Fatalf("BuildDefaultTun without exclusions: %v", err)
	}
	if err := defaultDevice.Close(); err != nil {
		t.Fatalf("close default TUN: %v", err)
	}
	assertSplitDriverStopped("after default TUN")
}

func TestWindowsSplitDependencyRejectionDoesNotProvisionService(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("split-driver service fixtures require an elevated disposable guest")
	}
	const serviceName = "mullvad-split-tunnel"
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Disconnect() })
	service, err := manager.OpenService(serviceName)
	if err != nil {
		t.Fatalf("open provisioned split-driver service: %v", err)
	}
	original, err := service.Config()
	if err != nil {
		_ = service.Close()
		t.Fatalf("read provisioned split-driver config: %v", err)
	}
	status, err := service.Query()
	if err != nil {
		_ = service.Close()
		t.Fatalf("query provisioned split-driver: %v", err)
	}
	if status.State != svc.Stopped {
		_ = service.Close()
		t.Fatalf("provisioned split-driver state = %v, want stopped", status.State)
	}

	attemptExcludedDefaultTun := func(name string) {
		t.Helper()
		executable, pathErr := filepath.Abs(os.Args[0])
		if pathErr != nil {
			t.Fatalf("resolve test executable: %v", pathErr)
		}
		system, buildErr := sysnetwindows.New(sysnetwindows.SystemConfig{AdapterNamePrefix: name})
		if buildErr != nil {
			t.Fatalf("windows.New: %v", buildErr)
		}
		device, buildErr := system.BuildDefaultTun(sysnet.DefaultTunOpts{
			TunAddrs: []string{"198.18.244.1/32"},
			Exclude:  []sysnet.Rule{{Type: "win-exe-tree", Rule: executable}},
		})
		if device != nil {
			_ = device.Close()
		}
		closeErr := system.Close()
		if buildErr == nil {
			t.Fatalf("BuildDefaultTun with rejected split dependency succeeded; close error: %v", closeErr)
		}
		if closeErr != nil {
			t.Fatalf("close after rejected split dependency: %v", closeErr)
		}
	}

	t.Run("stopped", func(t *testing.T) {
		attemptExcludedDefaultTun("alm-split-stop")
		got, err := service.Query()
		if err != nil {
			t.Fatal(err)
		}
		if got.State != svc.Stopped {
			t.Fatalf("service state = %v, want stopped", got.State)
		}
	})

	t.Run("incompatible", func(t *testing.T) {
		incompatible := original
		incompatible.BinaryPathName = `\SystemRoot\System32\drivers\incompatible-almagest-fixture.sys`
		if err := setWindowsTestServiceBinaryPath(service, incompatible.BinaryPathName); err != nil {
			t.Fatalf("set incompatible service config: %v", err)
		}
		t.Cleanup(func() {
			if err := setWindowsTestServiceBinaryPath(service, original.BinaryPathName); err != nil {
				t.Errorf("restore service config: %v", err)
			}
		})
		attemptExcludedDefaultTun("alm-split-bad")
		got, err := service.Config()
		if err != nil {
			t.Fatal(err)
		}
		if got.BinaryPathName != incompatible.BinaryPathName {
			t.Fatalf("service binary path = %q, want rejected fixture %q", got.BinaryPathName, incompatible.BinaryPathName)
		}
	})

	// Run the absent-service fixture last. Deletion is asynchronous, and the
	// cleanup recreates the exact provisioned service for diagnostics.
	if err := service.Delete(); err != nil {
		t.Fatalf("delete service fixture: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close deleted service fixture: %v", err)
	}
	service = nil
	t.Cleanup(func() {
		restored, err := manager.CreateService(serviceName, original.BinaryPathName, original)
		if err != nil {
			t.Errorf("restore deleted split-driver service: %v", err)
			return
		}
		_ = restored.Close()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		candidate, openErr := manager.OpenService(serviceName)
		if openErr != nil {
			break
		}
		_ = candidate.Close()
		time.Sleep(100 * time.Millisecond)
	}
	if candidate, openErr := manager.OpenService(serviceName); openErr == nil {
		_ = candidate.Close()
		t.Fatal("split-driver service was not deleted for the absent fixture")
	}
	attemptExcludedDefaultTun("alm-split-none")
	if candidate, openErr := manager.OpenService(serviceName); openErr == nil {
		_ = candidate.Close()
		t.Fatal("backend recreated the absent split-driver service")
	}
}

func setWindowsTestServiceBinaryPath(service *mgr.Service, path string) error {
	binaryPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ChangeServiceConfig(
		service.Handle,
		windows.SERVICE_NO_CHANGE,
		windows.SERVICE_NO_CHANGE,
		windows.SERVICE_NO_CHANGE,
		binaryPath,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
}
