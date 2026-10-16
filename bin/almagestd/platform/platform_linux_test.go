//go:build linux

package platform

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	almargs "github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/internal/nativebackend"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/configer/hujson"
	sysnetlinux "github.com/asciimoth/sysnet-linux"
)

func TestSystemIntegrationReturnsLinuxBackendWithoutProxy(t *testing.T) {
	// Set one feature so sysnet-linux does not replace the feature set with its
	// auto-detected defaults. This keeps the test independent of host network
	// privileges and services.
	cfg := configer.NewMemory(map[string]any{
		"Linux": map[string]any{
			"Features": map[string]any{"MatcherRules": true},
		},
	})

	system, err := SystemIntegration(cfg, nil, 1)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	backend, ok := system.(*sysnetlinux.System)
	if !ok {
		t.Fatalf("system = %T, want direct *linux.System", system)
	}
	if system.OutNet() != backend.OutNet() {
		t.Fatal("OutNet was replaced or wrapped outside sysnet-linux")
	}
	if system.LocalNet() != backend.LocalNet() {
		t.Fatal("LocalNet was replaced or wrapped outside sysnet-linux")
	}
	allocations := testing.AllocsPerRun(1000, func() {
		if system.OutNet() != backend.OutNet() || system.LocalNet() != backend.LocalNet() {
			panic("native network identity changed")
		}
	})
	if allocations != 0 {
		t.Fatalf("direct sysnet-linux network lookup allocations = %f, want 0", allocations)
	}
}

func TestSetupFSLinuxDefaultUsesHomeStorage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root default storage intentionally uses /var instead of home")
	}

	home := newSafePlatformTestDir(t)
	t.Setenv("HOME", home)

	fs, cleanup, err := SetupFS("", false)
	if err != nil {
		t.Fatalf("SetupFS: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	want := filepath.Join(home, ".local", "share", "almagest", "store")
	if fs != want {
		t.Fatalf("fs = %q, want %q", fs, want)
	}
	assertPrivateDir(t, fs)
}

func TestLinuxDefaultFileStoragePathUsesVarForRoot(t *testing.T) {
	if got := linuxDefaultFileStoragePathFor(0, "/root"); got != linuxFallbackFileStorage {
		t.Fatalf("linuxDefaultFileStoragePathFor(root) = %q, want %q", got, linuxFallbackFileStorage)
	}
}

func TestLinuxDefaultFileStoragePathUsesHomeForNonRoot(t *testing.T) {
	home := "/home/alice"
	want := filepath.Join(home, ".local", "share", "almagest", "store")
	if got := linuxDefaultFileStoragePathFor(1000, home); got != want {
		t.Fatalf("linuxDefaultFileStoragePathFor(non-root) = %q, want %q", got, want)
	}
}

func TestLinuxDefaultFileStoragePathUsesVarWithoutHome(t *testing.T) {
	if got := linuxDefaultFileStoragePathFor(1000, ""); got != linuxFallbackFileStorage {
		t.Fatalf("linuxDefaultFileStoragePathFor(no home) = %q, want %q", got, linuxFallbackFileStorage)
	}
}

func TestSetupFSLinuxRelativePathReturnsAbsoluteStorage(t *testing.T) {
	base := newSafePlatformTestDir(t)
	t.Chdir(base)

	fs, cleanup, err := SetupFS("rel-store", false)
	if err != nil {
		t.Fatalf("SetupFS: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	want := filepath.Join(base, "rel-store")
	if fs != want {
		t.Fatalf("fs = %q, want %q", fs, want)
	}
	assertPrivateDir(t, fs)
}

func TestSetupFSLinuxTemporaryIgnoresPathAndCleans(t *testing.T) {
	base := newSafePlatformTestDir(t)
	t.Setenv("XDG_RUNTIME_DIR", base)
	ignored := filepath.Join(base, "ignored")

	fs, cleanup, err := SetupFS(ignored, true)
	if err != nil {
		t.Fatalf("SetupFS: %v", err)
	}

	if fs == ignored {
		t.Fatalf("fs = ignored path %q, want generated temp path", fs)
	}
	if filepath.Dir(fs) != base {
		t.Fatalf("fs parent = %q, want %q", filepath.Dir(fs), base)
	}
	if !strings.HasPrefix(filepath.Base(fs), "alm-store-") {
		t.Fatalf("fs base = %q, want alm-store- prefix", filepath.Base(fs))
	}
	assertPrivateDir(t, fs)

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(fs); !os.IsNotExist(err) {
		t.Fatalf("temporary storage still exists after cleanup: %v", err)
	}
}

func TestTmpAdminUnixSocketPathLinuxUsesRuntimeDir(t *testing.T) {
	base := newSafePlatformTestDir(t)
	t.Setenv("XDG_RUNTIME_DIR", base)

	socketPath, err := TmpAdminUnixSocketPath()
	if err != nil {
		t.Fatalf("TmpAdminUnixSocketPath: %v", err)
	}

	if filepath.Dir(socketPath) != base {
		t.Fatalf("socket parent = %q, want %q", filepath.Dir(socketPath), base)
	}
	if !strings.HasPrefix(filepath.Base(socketPath), tmpAdminUnixSocketPrefix) {
		t.Fatalf("socket base = %q, want %s prefix", filepath.Base(socketPath), tmpAdminUnixSocketPrefix)
	}
}

func TestSetupFSLinuxRejectsLooseStorageDirectory(t *testing.T) {
	base := newSafePlatformTestDir(t)
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if _, _, err := SetupFS(loose, false); err == nil {
		t.Fatal("SetupFS succeeded for storage directory with loose permissions")
	}
}

func TestReadLinuxSystemConfigMatchesBrokerParser(t *testing.T) {
	for _, document := range []string{`{}`, `{"UserMark":"0x1234","AppBypassMask":0,"DNS":{"FallbackServers":["::1"]},"Pmark":{"PinPath":"/run/bpf"},"KillswitchAllowExclude":false}`} {
		cfg, err := hujson.Parse([]byte(`{"Linux":` + document + `}`))
		if err != nil {
			t.Fatal(err)
		}
		direct, err := readLinuxSystemConfig(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		broker, err := nativebackend.ParseLinuxFileConfig([]byte(document))
		if err != nil {
			t.Fatal(err)
		}
		direct.Logf = nil
		if !reflect.DeepEqual(direct, broker.Native(nil)) {
			t.Fatalf("direct and broker settings differ for %s", document)
		}
	}
}

func TestReadLinuxSystemConfigDefaults(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := readLinuxSystemConfig(cfg, nil)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}

	if got.Pmark.PinPath != "/sys/fs/bpf/almagest" {
		t.Fatalf("Pmark.PinPath = %q, want default", got.Pmark.PinPath)
	}
	if got.UserMark != linuxDefaultUserMark {
		t.Fatalf("UserMark = %#x, want %#x", got.UserMark, linuxDefaultUserMark)
	}
	if !got.KillswitchAllowExclude {
		t.Fatal("KillswitchAllowExclude = false, want true")
	}
	wantFallback := []netip.AddrPort{
		netip.MustParseAddrPort("8.8.8.8:53"),
		netip.MustParseAddrPort("1.1.1.1:53"),
	}
	if len(got.DNS.FallbackServers) != len(wantFallback) {
		t.Fatalf("FallbackServers = %v, want %v", got.DNS.FallbackServers, wantFallback)
	}
	for i, want := range wantFallback {
		if got.DNS.FallbackServers[i] != want {
			t.Fatalf("FallbackServers[%d] = %v, want %v", i, got.DNS.FallbackServers[i], want)
		}
	}
	if got.Logf == nil {
		t.Fatal("Logf = nil, want golog.Debugf")
	}
}

func TestReadLinuxSystemConfigOverrides(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"Pmark": {
				"PinPath": "/run/almagest-bpf",
				"Callbacks": {
					"Logf": "ignored for now"
				}
			},
			"DNS": {
				"Mode": "direct",
				"FallbackServers": ["9.9.9.9", "149.112.112.112:5353"]
			},
			"KillswitchAllowExclude": false,
			"AppBypassMark": "0xeb9f0001",
			"AppBypassMask": "ffffffff",
			"UserMark": 1095527682
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := readLinuxSystemConfig(cfg, nil)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}

	if got.Pmark.PinPath != "/run/almagest-bpf" {
		t.Fatalf("Pmark.PinPath = %q, want override", got.Pmark.PinPath)
	}
	if got.DNS.Mode != "direct" {
		t.Fatalf("DNS.Mode = %q, want direct", got.DNS.Mode)
	}
	wantFallback := []netip.AddrPort{
		netip.MustParseAddrPort("9.9.9.9:53"),
		netip.MustParseAddrPort("149.112.112.112:5353"),
	}
	for i, want := range wantFallback {
		if got.DNS.FallbackServers[i] != want {
			t.Fatalf("FallbackServers[%d] = %v, want %v", i, got.DNS.FallbackServers[i], want)
		}
	}
	if got.KillswitchAllowExclude {
		t.Fatal("KillswitchAllowExclude = true, want false override")
	}
	if got.AppBypassMark != 0xeb9f0001 {
		t.Fatalf("AppBypassMark = %#x, want override", got.AppBypassMark)
	}
	if got.AppBypassMask != 0xffffffff {
		t.Fatalf("AppBypassMask = %#x, want override", got.AppBypassMask)
	}
	if got.UserMark != 1095527682 {
		t.Fatalf("UserMark = %#x, want override", got.UserMark)
	}
}

func TestReadLinuxSystemConfigHexMarkStrings(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"AppBypassMark": "0xeb9f0001",
			"AppBypassMask": "ffffffff",
			"UserMark": "0x414c4d01",
			"UserMarkMask": "FFFFFFFF"
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := readLinuxSystemConfig(cfg, nil)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}

	if got.AppBypassMark != 0xeb9f0001 {
		t.Fatalf("AppBypassMark = %#x, want 0xeb9f0001", got.AppBypassMark)
	}
	if got.AppBypassMask != 0xffffffff {
		t.Fatalf("AppBypassMask = %#x, want 0xffffffff", got.AppBypassMask)
	}
	if got.UserMark != 0x414c4d01 {
		t.Fatalf("UserMark = %#x, want 0x414c4d01", got.UserMark)
	}
	if got.UserMarkMask != 0xffffffff {
		t.Fatalf("UserMarkMask = %#x, want 0xffffffff", got.UserMarkMask)
	}
}

func TestReadLinuxSystemConfigCallbacks(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"Callbacks": {
				"DNSConfigured": {
					"Command": "notify-dns",
					"Slots": {
						"callback": "{{ .Callback }}",
						"server": "{{ .Server }}"
					}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	runner := &recordingDispatcher{}

	got, err := readLinuxSystemConfig(cfg, runner)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}
	if got.Callbacks.DNSConfigured == nil {
		t.Fatal("DNSConfigured callback = nil, want command callback")
	}

	got.Callbacks.DNSConfigured(netip.MustParseAddr("192.0.2.53"))

	if len(runner.requests) != 1 {
		t.Fatalf("submitted requests = %d, want 1", len(runner.requests))
	}
	req := runner.requests[0]
	if req.Name != "notify-dns" {
		t.Fatalf("request command name = %q, want notify-dns", req.Name)
	}
	if req.Label != linuxCallbackDNSConfigured {
		t.Fatalf("request label = %q, want %q", req.Label, linuxCallbackDNSConfigured)
	}
	if got := req.Slots["callback"]; got != linuxCallbackDNSConfigured {
		t.Fatalf("request slot callback = %q, want %q", got, linuxCallbackDNSConfigured)
	}
	if got := req.Slots["server"]; got != "192.0.2.53" {
		t.Fatalf("request slot server = %q, want 192.0.2.53", got)
	}
}

func TestReadLinuxSystemConfigCallbacksRequireRunner(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"Callbacks": {
				"DefaultTunClosed": {
					"Command": "notify-close"
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	_, err = readLinuxSystemConfig(cfg, nil)
	if err == nil {
		t.Fatal("readLinuxSystemConfig succeeded without callback runner")
	}
}

func TestSafeFileLifecycle(t *testing.T) {
	base := newSafePlatformTestDir(t)
	dir := filepath.Join(base, "config", "nested")
	path := filepath.Join(dir, "almagest.hujson")
	initial := []byte("{value: 1}\n")

	if err := MakeSureSafeFileExists(path, initial); err != nil {
		t.Fatalf("MakeSureSafeFileExists: %v", err)
	}
	if err := MakeSureSafeFileExists(path, []byte("must not replace")); err != nil {
		t.Fatalf("MakeSureSafeFileExists existing: %v", err)
	}
	got, err := ReadSafeFile(path)
	if err != nil || string(got) != string(initial) {
		t.Fatalf("ReadSafeFile = %q, %v", got, err)
	}
	f, err := OpenSafeFile(path)
	if err != nil {
		t.Fatalf("OpenSafeFile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	isDir, err := IsDir(dir)
	if err != nil || !isDir {
		t.Fatalf("IsDir(%q) = %t, %v", dir, isDir, err)
	}
	isDir, err = IsDir(filepath.Join(base, "missing"))
	if err != nil || isDir {
		t.Fatalf("IsDir(missing) = %t, %v", isDir, err)
	}

	emptyDir := filepath.Join(base, "created", "deep")
	if err := MakeSureDirExists(emptyDir); err != nil {
		t.Fatalf("MakeSureDirExists: %v", err)
	}
	if err := checkPrivateDirPath(emptyDir); err != nil {
		t.Fatalf("checkPrivateDirPath: %v", err)
	}
}

func TestSafePathValidation(t *testing.T) {
	badPaths := []string{"", "relative", "/a/../b"}
	for _, path := range badPaths {
		if _, err := splitAbsPath(path); err == nil {
			t.Errorf("splitAbsPath(%q) succeeded", path)
		}
	}
	if parts, err := splitAbsPath("/"); err != nil || parts != nil {
		t.Fatalf("splitAbsPath(/) = %#v, %v", parts, err)
	}
	if parts, err := splitAbsPath("//etc///almagest"); err != nil || len(parts) != 2 || parts[0] != "etc" || parts[1] != "almagest" {
		t.Fatalf("splitAbsPath cleaned = %#v, %v", parts, err)
	}
	for _, path := range []string{"/", "/tmp/"} {
		if _, _, err := splitAbsFilePath(path); err == nil {
			t.Errorf("splitAbsFilePath(%q) succeeded", path)
		}
	}
	parents, base, err := splitAbsFilePath("/etc/almagest/config")
	if err != nil || base != "config" || len(parents) != 2 {
		t.Fatalf("splitAbsFilePath = %#v, %q, %v", parents, base, err)
	}
}

func TestSafeFilesRejectSymlinksAndLooseModes(t *testing.T) {
	base := newSafePlatformTestDir(t)
	target := filepath.Join(base, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSafeFile(link); err == nil {
		t.Fatal("OpenSafeFile accepted a symlink")
	}

	loose := filepath.Join(base, "loose")
	if err := os.WriteFile(loose, []byte("public"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSafeFile(loose); err == nil {
		t.Fatal("ReadSafeFile accepted a group-writable file")
	}

	dirAsFile := filepath.Join(base, "directory")
	if err := os.Mkdir(dirAsFile, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSafeFile(dirAsFile); err == nil {
		t.Fatal("OpenSafeFile accepted a directory")
	}
	if err := MakeSureSafeFileExists(dirAsFile, nil); err == nil {
		t.Fatal("MakeSureSafeFileExists accepted a directory")
	}
}

func TestLinuxAgentReadsAndWritesUnsafeConfig(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "alm.hujson")
	protectedPath := filepath.Join(base, "protected.hujson")
	if err := os.WriteFile(configPath, []byte("{value: 1}"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protectedPath, []byte("{protected: true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := RunAgent(almargs.Args{Config: configPath, ProtectedConfig: protectedPath, UnsafeConfig: true})
	if agent.ConfRO() || agent.ConfigFilePath() != configPath || agent.ProtectedConfigFilePath() != protectedPath {
		t.Fatalf("agent paths/read-only = %q, %q, %t", agent.ConfigFilePath(), agent.ProtectedConfigFilePath(), agent.ConfRO())
	}
	config, protected, err := agent.ReadConfig()
	if err != nil || string(config) != "{value: 1}" || string(protected) != "{protected: true}" {
		t.Fatalf("ReadConfig = %q, %q, %v", config, protected, err)
	}
	if err := agent.WriteConfig([]byte("{value: 2}")); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(configPath)
	if err != nil || string(written) != "{value: 2}" {
		t.Fatalf("written config = %q, %v", written, err)
	}
}

func TestPrepareConfigSeparatesInputsAndUsesFixedTargetCompareReplace(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "alm.hujson")
	protectedPath := filepath.Join(base, "protected.hujson")
	if err := os.WriteFile(configPath, []byte("{value: 1}"), 0o600); err != nil {
		t.Fatal(err)
	}
	protected := []byte("{secret: true}")
	if err := os.WriteFile(protectedPath, protected, 0o600); err != nil {
		t.Fatal(err)
	}

	bootstrap, err := PrepareConfig(almargs.Args{
		Config: configPath, ProtectedConfig: protectedPath, UnsafeConfig: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bootstrap.Regular) != "{value: 1}" || string(bootstrap.Protected) != string(protected) {
		t.Fatalf("bootstrap inputs = %q, %q", bootstrap.Regular, bootstrap.Protected)
	}
	if bootstrap.Persistence == nil {
		t.Fatal("writable config has no persistence service")
	}
	current, err := bootstrap.Persistence.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	result := bootstrap.Persistence.Replace(current.Revision, "replace-1", []byte("{value: 2}"))
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}

	// An external edit changes the file revision. The runtime cannot overwrite
	// it by sending the old revision or select another target in the request.
	if err := os.WriteFile(configPath, []byte("{external: true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	conflict := bootstrap.Persistence.Replace(result.Revision, "replace-2", []byte("{value: 3}"))
	if !errors.Is(conflict.Err(), daemonservices.ErrConfigRevisionConflict) {
		t.Fatalf("external edit result = %#v", conflict)
	}
	writtenProtected, err := os.ReadFile(protectedPath)
	if err != nil || !bytes.Equal(writtenProtected, protected) {
		t.Fatalf("protected config changed: %q, %v", writtenProtected, err)
	}
}

func TestLinuxAgentWriteConfigReplacesFileAtomically(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "alm.hujson")
	if err := os.WriteFile(configPath, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}

	agent := &agent{ConfigPath: configPath}
	if err := agent.WriteConfig([]byte("after")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("WriteConfig updated the live inode instead of replacing it")
	}
	if after.Mode().Perm() != 0o640 {
		t.Fatalf("config permissions = %o, want 640", after.Mode().Perm())
	}
	written, err := os.ReadFile(configPath)
	if err != nil || string(written) != "after" {
		t.Fatalf("written config = %q, %v", written, err)
	}
	matches, err := filepath.Glob(filepath.Join(base, ".alm.hujson.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary config files remain: %v", matches)
	}
}

func TestLinuxAgentWriteConfigPreservesSymlink(t *testing.T) {
	base := t.TempDir()
	targetPath := filepath.Join(base, "stored.hujson")
	configPath := filepath.Join(base, "alm.hujson")
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(targetPath), configPath); err != nil {
		t.Fatal(err)
	}

	agent := &agent{ConfigPath: configPath}
	if err := agent.WriteConfig([]byte("after")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("WriteConfig replaced the configured symlink")
	}
	written, err := os.ReadFile(targetPath)
	if err != nil || string(written) != "after" {
		t.Fatalf("symlink target content = %q, %v", written, err)
	}
}

func TestLinuxAgentWriteConfigNeverExposesPartialDocument(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "alm.hujson")
	first := []byte(`{"generation":"first","padding":"` + strings.Repeat("a", 512<<10) + `"}`)
	second := []byte(`{"generation":"second","padding":"` + strings.Repeat("b", 512<<10) + `"}`)
	if err := os.WriteFile(configPath, first, 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &agent{ConfigPath: configPath}
	const readerCount = 4
	var ready sync.WaitGroup
	var readers sync.WaitGroup
	ready.Add(readerCount)
	readers.Add(readerCount)
	stop := make(chan struct{})
	errs := make(chan error, 1)
	for range readerCount {
		go func() {
			defer readers.Done()
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(configPath)
				if err == nil && (bytes.Equal(data, first) || bytes.Equal(data, second)) {
					continue
				}
				if err == nil {
					err = fmt.Errorf("read partial config with %d bytes", len(data))
				}
				select {
				case errs <- err:
				default:
				}
				return
			}
		}()
	}
	ready.Wait()
	for index := range 64 {
		data := first
		if index%2 == 0 {
			data = second
		}
		if err := agent.WriteConfig(data); err != nil {
			close(stop)
			readers.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

func TestLinuxAgentUsesConfigDirectoryAndDefaultProtectedPath(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "alm.hujson")
	protectedPath := filepath.Join(base, "protected_alm.hujson")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protectedPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := RunAgent(almargs.Args{Config: base, UnsafeConfig: true})
	if agent.ConfigFilePath() != configPath || agent.ProtectedConfigFilePath() != protectedPath {
		t.Fatalf("agent paths = %q, %q", agent.ConfigFilePath(), agent.ProtectedConfigFilePath())
	}
}

func TestAgentReadAndWriteErrors(t *testing.T) {
	a := &agent{UnsafeConfig: true, ConfigPath: filepath.Join(t.TempDir(), "missing"), ProtectedPath: "missing"}
	if _, _, err := a.ReadConfig(); err == nil {
		t.Fatal("ReadConfig missing config succeeded")
	}
	if err := a.WriteConfig([]byte("data")); err == nil {
		t.Fatal("WriteConfig missing config succeeded")
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.ConfigPath = path
	a.ProtectedPath = filepath.Join(t.TempDir(), "missing")
	if _, _, err := a.ReadConfig(); err == nil {
		t.Fatal("ReadConfig missing protected config succeeded")
	}
}

type recordingDispatcher struct {
	requests []recordedCommandRequest
}

type recordedCommandRequest struct {
	cmdrunner.Request
}

func (d *recordingDispatcher) SubmitBackendCallback(req cmdrunner.Request) bool {
	d.requests = append(d.requests, recordedCommandRequest{Request: req})
	return true
}

func newSafePlatformTestDir(t *testing.T) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := MakeSureDirExists(cwd); err != nil {
		t.Skipf("current test directory is not safe for platform storage tests: %v", err)
	}

	dir, err := os.MkdirTemp(cwd, ".almagest-platform-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	return dir
}

func assertPrivateDir(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", path)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("%q mode = %#o, want 0700", path, mode)
	}
}
