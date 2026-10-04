//go:build windows && winintegration

package integration_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/asciimoth/almagest/bin/api"
	"golang.org/x/sys/windows"
)

const integrationTimeout = 45 * time.Second

const commandMarkerEnvironment = "ALMAGEST_WINDOWS_DAEMON_COMMAND_MARKER"

type windowsDaemon struct {
	t         *testing.T
	root      string
	daemon    string
	cli       string
	pipe      string
	endpoint  string
	config    string
	protected string
	storage   string
	command   *exec.Cmd
	log       *os.File
}

func TestWindowsDaemonNativeAdminWorkflow(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	httpPort := reserveTCPPort(t)
	socksPort := reserveTCPPort(t)
	pipe := fmt.Sprintf("almagest-e2e-%d", os.Getpid())
	commandMarker := filepath.Join(root, "command-marker.txt")
	config := fmt.Sprintf(`{
	"Config":{"Save":{"IdleMilliseconds":10}},
	"nodes":{
		"Fixture Network":{"Class":"almagest/MockNetwork"},
		"Fixture HTTP":{"Class":"almagest/HttpServer","Links":["Network -> [Fixture Network] Network"],"host":"127.0.0.1","port":%d,"response":"windows-native-e2e"},
		"Fixture SOCKS":{"Class":"almagest/LocalProxy","Links":["proxy -> [Fixture Network] Network"],"socksHost":"127.0.0.1","socksPort":%d},
		"Fixture Command":{"Class":"almagest/Command","command":"record-marker"}
	}}
`, httpPort, socksPort)
	daemon := newWindowsDaemon(t, root, pipe, config, workflowProtectedConfig(pipe, "alm-e2e", commandMarker))
	daemon.start()

	daemon.cliJSON("admin", "ping", "--nonce", "native-daemon")
	wantSOCKSHTTP(t, socksPort, httpPort, "windows-native-e2e")
	triggerCommandNode(t, daemon, commandMarker)
	if output := daemon.cliJSON("tls", "builtin-ca"); !bytes.Contains(output, []byte("fingerprint")) {
		t.Fatalf("TLS built-in CA response has no fingerprint: %s", output)
	}

	watchLog := filepath.Join(root, "watch.log")
	watch := daemon.cliCommand("engine", "watch", "--full-snapshot")
	watchFile, err := os.Create(watchLog)
	if err != nil {
		t.Fatal(err)
	}
	watch.Stdout, watch.Stderr = watchFile, watchFile
	if err = watch.Start(); err != nil {
		_ = watchFile.Close()
		t.Fatalf("start production CLI subscription: %v", err)
	}
	watchStopped := false
	t.Cleanup(func() {
		if !watchStopped {
			_ = watch.Process.Kill()
			_ = watch.Wait()
		}
		_ = watchFile.Close()
	})
	waitFor(t, "full engine subscription snapshot", func() bool {
		data, readErr := os.ReadFile(watchLog)
		return readErr == nil && bytes.Contains(data, []byte(`"workspace"`))
	})
	if err = watch.Process.Kill(); err != nil {
		t.Fatalf("stop production CLI subscription: %v", err)
	}
	if err = watch.Wait(); err == nil {
		t.Fatal("production CLI subscription exited successfully after forced cancellation")
	}
	watchStopped = true
	if err = watchFile.Close(); err != nil {
		t.Fatalf("close production CLI subscription log: %v", err)
	}

	created := struct {
		UUID string `json:"uuid"`
	}{}
	decodeJSON(t, daemon.cliJSON("snapshots", "create", "--name", "windows native daemon"), &created)
	if created.UUID == "" {
		t.Fatal("snapshot create returned an empty UUID")
	}
	mutatedPath := filepath.Join(root, "mutated.hujson")
	writePrivateWindowsFile(t, mutatedPath, []byte(`{"nodes":{"Mutated":{"Class":"almagest/MockNetwork"}}}`))
	daemon.cliJSON("config", "apply", mutatedPath)
	if output := daemon.cliJSON("graph", "map"); !bytes.Contains(output, []byte("Mutated")) {
		t.Fatalf("live config edit is absent from graph: %s", output)
	}
	daemon.cliJSON("snapshots", "restore", created.UUID)
	wantSOCKSHTTP(t, socksPort, httpPort, "windows-native-e2e")

	daemon.stop()
	daemon.start()
	defer daemon.stop()
	wantSOCKSHTTP(t, socksPort, httpPort, "windows-native-e2e")
	triggerCommandNode(t, daemon, commandMarker)
	if output := daemon.cliJSON("config", "get"); !bytes.Contains(output, []byte("Fixture SOCKS")) {
		t.Fatalf("restored config did not persist across restart: %s", output)
	}
}

func TestWindowsDaemonCommandHelper(t *testing.T) {
	marker := os.Getenv(commandMarkerEnvironment)
	if marker == "" {
		return
	}
	file, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Fprintln(file, "windows-command"); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowsDaemonMachineTrustedBuiltinCAInterceptsTLS(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("machine trusted-root installation requires an elevated test identity")
	}
	root := newPrivateWindowsTestDir(t)
	httpPort := reserveTCPPort(t)
	socksPort := reserveTCPPort(t)
	pipe := fmt.Sprintf("almagest-tls-trust-%d", os.Getpid())
	config := fmt.Sprintf(`{
	"nodes":{
		"Fixture Network":{"Class":"almagest/MockNetwork","allowAnyHost":true},
		"Fixture HTTP":{"Class":"almagest/HttpServer","Links":["Network -> [Fixture Network] Network"],"host":"127.0.0.1","port":%d,"response":"windows-trusted-ca-mitm"},
		"Fixture Terminator":{"Class":"almagest/TlsTerminator","Links":["Network -> [Fixture Network] Network"],"useSNIHostname":false,"serverAlpn":"http/1.1"},
		"Fixture MITM":{"Class":"almagest/TlsMitm","Links":["Network -> [Fixture Terminator] Network"],"filterMode":"off"},
		"Fixture SOCKS":{"Class":"almagest/LocalProxy","Links":["proxy -> [Fixture MITM] Network"],"socksHost":"127.0.0.1","socksPort":%d}
	}}
`, httpPort, socksPort)
	daemon := newWindowsDaemon(t, root, pipe, config, nativeProtectedConfig(pipe, "alm-tls-trust"))
	daemon.start()
	defer daemon.stop()

	info := api.TLSBuiltinCAInfo{}
	decodeJSON(t, daemon.cliJSON("tls", "builtin-ca"), &info)
	if info.Cert.PEM == "" || info.Cert.SerialNumber == "" || info.CRL.PEM == "" {
		t.Fatalf("TLS built-in CA has incomplete certificate metadata: %#v", info.Cert)
	}
	caPath := filepath.Join(root, "almagest-builtin-ca.pem")
	crlPath := filepath.Join(root, "almagest-builtin-ca.crl.pem")
	writePrivateWindowsFile(t, caPath, []byte(info.Cert.PEM))
	writePrivateWindowsFile(t, crlPath, []byte(info.CRL.PEM))

	curlVersion, err := exec.Command("curl.exe", "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect Windows curl.exe: %v\n%s", err, curlVersion)
	}
	if !bytes.Contains(bytes.ToLower(curlVersion), []byte("schannel")) {
		t.Fatalf("Windows curl.exe does not use Schannel: %s", curlVersion)
	}

	url := fmt.Sprintf("https://tls-mitm.local:%d/intercepted", httpPort)
	curl := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "curl.exe",
			"--fail", "--silent", "--show-error", "--http1.1",
			"--connect-timeout", "3", "--max-time", "10",
			"--socks5-hostname", fmt.Sprintf("127.0.0.1:%d", socksPort),
			url,
		).CombinedOutput()
	}

	if output, curlErr := curl(); curlErr == nil {
		t.Fatalf("curl.exe trusted the generated interception CA before installation: %s", output)
	} else {
		t.Logf("curl.exe before CA installation failed as expected: %v: %s", curlErr, output)
	}
	install := exec.Command("certutil.exe", "-f", "-addstore", "Root", caPath)
	if output, installErr := install.CombinedOutput(); installErr != nil {
		t.Fatalf("install built-in CA in machine Root store: %v\n%s", installErr, output)
	}
	installCRL := exec.Command("certutil.exe", "-f", "-addstore", "CA", crlPath)
	if output, installErr := installCRL.CombinedOutput(); installErr != nil {
		t.Fatalf("install built-in CA revocation list in machine CA store: %v\n%s", installErr, output)
	}
	t.Cleanup(func() {
		removeCRL := exec.Command("certutil.exe", "-f", "-delstore", "CA", info.Options.Issuer)
		if output, removeErr := removeCRL.CombinedOutput(); removeErr != nil {
			t.Errorf("remove built-in CA revocation list from machine CA store: %v\n%s", removeErr, output)
		}
		remove := exec.Command("certutil.exe", "-f", "-delstore", "Root", info.Cert.SerialNumber)
		if output, removeErr := remove.CombinedOutput(); removeErr != nil {
			t.Errorf("remove built-in CA from machine Root store: %v\n%s", removeErr, output)
		}
	})

	deadline := time.Now().Add(integrationTimeout)
	var lastOutput []byte
	var lastErr error
	for time.Now().Before(deadline) {
		lastOutput, lastErr = curl()
		if lastErr == nil && string(lastOutput) == "windows-trusted-ca-mitm" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("trusted curl.exe request through TLS interception: %v: %s\ndaemon log:\n%s", lastErr, lastOutput, daemon.readLog())
}

func TestWindowsDaemonNativeResourceBaselineRestored(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native adapter inventory requires an elevated test identity")
	}
	root := newPrivateWindowsTestDir(t)
	prefix := fmt.Sprintf("alme2e%d", os.Getpid())
	pipe := fmt.Sprintf("almagest-resource-%d", os.Getpid())
	before := ownedNetworkState(t, prefix)
	if len(before) != 0 {
		t.Fatalf("owned native state exists before daemon start: %v", before)
	}
	config := `{"nodes":{"Native Tun":{"Class":"almagest/Tun","tunAddrs":"198.18.239.1/32","tunRoutes":"198.19.239.0/24","mtu":1380}}}`
	daemon := newWindowsDaemon(t, root, pipe, config, nativeProtectedConfig(pipe, prefix))
	daemon.start()
	waitFor(t, "native adapter creation", func() bool { return len(ownedNetworkState(t, prefix)) != 0 })
	daemon.cliJSON("admin", "ping", "--nonce", "native-resource")
	daemon.stop()
	waitFor(t, "native adapter, route, and DNS cleanup", func() bool {
		return slices.Equal(ownedNetworkState(t, prefix), before)
	})

	// Repeat the production startup path with the same persistent config. This
	// catches stale adapter ownership that only appears after restart.
	daemon.start()
	waitFor(t, "native adapter recreation", func() bool { return len(ownedNetworkState(t, prefix)) != 0 })
	daemon.stop()
	waitFor(t, "native state cleanup after restart", func() bool {
		return slices.Equal(ownedNetworkState(t, prefix), before)
	})
}

func TestWindowsDaemonNativeRepeatedGraphChanges(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native adapter inventory requires an elevated test identity")
	}
	root := newPrivateWindowsTestDir(t)
	prefix := fmt.Sprintf("almcycle%d", os.Getpid())
	pipe := fmt.Sprintf("almagest-cycle-%d", os.Getpid())
	before := ownedNetworkState(t, prefix)
	daemon := newWindowsDaemon(t, root, pipe, `{}`, nativeProtectedConfig(pipe, prefix))
	daemon.start()
	defer daemon.stop()

	emptyPath := filepath.Join(root, "empty.hujson")
	tunPath := filepath.Join(root, "tun.hujson")
	writePrivateWindowsFile(t, emptyPath, []byte(`{}`))
	writePrivateWindowsFile(t, tunPath, []byte(`{"nodes":{"Native Tun":{"Class":"almagest/Tun","tunAddrs":"198.18.238.1/32","tunRoutes":"198.19.238.0/24","mtu":1380}}}`))
	for cycle := range 25 {
		daemon.cliJSON("config", "apply", tunPath)
		waitFor(t, fmt.Sprintf("native adapter creation in cycle %d", cycle), func() bool {
			return len(ownedNetworkState(t, prefix)) != 0
		})
		daemon.cliJSON("config", "apply", emptyPath)
		waitFor(t, fmt.Sprintf("native cleanup in cycle %d", cycle), func() bool {
			return slices.Equal(ownedNetworkState(t, prefix), before)
		})
	}
}

func TestWindowsDaemonNativeForcedTerminationAndRestart(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native adapter inventory requires an elevated test identity")
	}
	root := newPrivateWindowsTestDir(t)
	prefix := fmt.Sprintf("almcrash%d", os.Getpid())
	pipe := fmt.Sprintf("almagest-crash-%d", os.Getpid())
	before := ownedNetworkState(t, prefix)
	config := `{"nodes":{"Native Tun":{"Class":"almagest/Tun","tunAddrs":"198.18.237.1/32","tunRoutes":"198.19.237.0/24","mtu":1380}}}`
	daemon := newWindowsDaemon(t, root, pipe, config, nativeProtectedConfig(pipe, prefix))
	daemon.start()
	waitFor(t, "native adapter before forced termination", func() bool {
		return len(ownedNetworkState(t, prefix)) != 0
	})
	daemon.kill()

	// The next production startup performs the backend's documented abandoned
	// Wintun scan. Almagest does not claim a persistent split/WFP journal.
	daemon.start()
	waitFor(t, "native adapter after forced termination recovery", func() bool {
		return len(ownedNetworkState(t, prefix)) != 0
	})
	daemon.cliJSON("admin", "ping", "--nonce", "forced-restart")
	daemon.stop()
	waitFor(t, "native cleanup after forced termination recovery", func() bool {
		return slices.Equal(ownedNetworkState(t, prefix), before)
	})
}

func TestWindowsDaemonNativeStartupFailures(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	pipe := fmt.Sprintf("almagest-failure-%d", os.Getpid())
	daemon := newWindowsDaemon(t, root, pipe, `{broken`, nativeProtectedConfig(pipe, "alm-failure"))
	command := daemon.commandForStart()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitCommand(command, integrationTimeout); err == nil {
		t.Fatal("daemon accepted malformed startup config")
	}

	writePrivateWindowsFile(t, daemon.config, []byte(`{}`))
	command = daemon.commandForStart()
	command.Env = append(os.Environ(), `SYSNET_WINDOWS_WINTUN_DLL=C:\definitely-absent\wintun.dll`)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitCommand(command, integrationTimeout); err == nil {
		t.Fatal("daemon accepted an absent explicitly selected Wintun DLL")
	}
}

func newWindowsDaemon(t *testing.T, root, pipe, config, protected string) *windowsDaemon {
	t.Helper()
	daemonPath, cliPath := buildWindowsBinaries(t, root)
	d := &windowsDaemon{
		t: t, root: root, daemon: daemonPath, cli: cliPath, pipe: pipe,
		config: filepath.Join(root, "alm.hujson"), protected: filepath.Join(root, "protected_alm.hujson"),
		storage: filepath.Join(root, "store"),
	}
	writePrivateWindowsFile(t, d.config, []byte(config))
	writePrivateWindowsFile(t, d.protected, []byte(protected))
	if isLocalSystem(t) {
		d.endpoint = "pipe://machine/" + pipe
	} else {
		d.endpoint = "pipe://user/" + pipe
	}
	return d
}

func (d *windowsDaemon) commandForStart() *exec.Cmd {
	command := exec.Command(d.daemon,
		"--config", d.config, "--protected", d.protected,
		"--file-storage", d.storage, "--pipe", `\\.\pipe\`+d.pipe,
		"--web", "0", "--debug-nodes", "--lvl", "debug")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	return command
}

func (d *windowsDaemon) start() {
	d.t.Helper()
	if d.command != nil {
		d.t.Fatal("daemon is already started")
	}
	logPath := filepath.Join(d.root, fmt.Sprintf("daemon-%d.log", time.Now().UnixNano()))
	log, err := os.Create(logPath)
	if err != nil {
		d.t.Fatal(err)
	}
	command := d.commandForStart()
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		_ = log.Close()
		d.t.Fatalf("start production daemon: %v", err)
	}
	d.command, d.log = command, log
	d.t.Cleanup(func() {
		if d.command != nil {
			_ = d.command.Process.Kill()
			_, _ = d.command.Process.Wait()
			d.command = nil
		}
		if d.log != nil {
			_ = d.log.Close()
			d.log = nil
		}
	})
	waitFor(d.t, "production daemon named pipe", func() bool {
		probe := d.cliCommand("admin", "ping", "--nonce", "ready")
		return probe.Run() == nil
	})
}

func (d *windowsDaemon) stop() {
	d.t.Helper()
	if d.command == nil {
		return
	}
	command := d.command
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(command.Process.Pid)); err != nil {
		d.t.Fatalf("signal production daemon: %v", err)
	}
	if err := waitCommand(command, integrationTimeout); err != nil {
		d.t.Fatalf("production daemon did not stop cleanly: %v\n%s", err, d.readLog())
	}
	d.command = nil
	if err := d.log.Close(); err != nil {
		d.t.Errorf("close daemon log: %v", err)
	}
	d.log = nil
}

func (d *windowsDaemon) kill() {
	d.t.Helper()
	if d.command == nil {
		return
	}
	command := d.command
	if err := command.Process.Kill(); err != nil {
		d.t.Fatalf("force production daemon termination: %v", err)
	}
	if err := waitCommand(command, integrationTimeout); err == nil {
		d.t.Fatal("force-terminated production daemon exited successfully")
	}
	d.command = nil
	if err := d.log.Close(); err != nil {
		d.t.Errorf("close force-terminated daemon log: %v", err)
	}
	d.log = nil
}

func (d *windowsDaemon) cliCommand(arguments ...string) *exec.Cmd {
	args := append([]string{"--url", d.endpoint}, arguments...)
	command := exec.Command(d.cli, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return command
}

func (d *windowsDaemon) cliJSON(arguments ...string) []byte {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	args := append([]string{"--url", d.endpoint}, arguments...)
	command := exec.CommandContext(ctx, d.cli, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		d.t.Fatalf("production CLI %q: %v\n%s\ndaemon log:\n%s", strings.Join(arguments, " "), err, output, d.readLog())
	}
	return output
}

func (d *windowsDaemon) readLog() string {
	if d.log != nil {
		_ = d.log.Sync()
	}
	data, _ := os.ReadFile(d.log.Name())
	return string(data)
}

func buildWindowsBinaries(t *testing.T, target string) (string, string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot find integration test source")
	}
	binRoot := filepath.Dir(filepath.Dir(file))
	daemon := filepath.Join(target, "almagestd.exe")
	cli := filepath.Join(target, "almagest-cli.exe")
	for output, pkg := range map[string]string{daemon: "./almagestd", cli: "./almagest-cli"} {
		command := exec.Command("go", "build", "-o", output, pkg)
		command.Dir = binRoot
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build production binary %s: %v\n%s", pkg, err, data)
		}
	}
	return daemon, cli
}

func nativeProtectedConfig(pipe, prefix string) string {
	return fmt.Sprintf(`{"Admin":{"Pipe":"\\\\.\\pipe\\%s"},"System":{"Type":"native","Windows":{"AdapterNamePrefix":"%s","OperationTimeout":"15s","Recovery":"inspect"}}}`, pipe, prefix)
}

func workflowProtectedConfig(pipe, prefix, marker string) string {
	helper, err := os.Executable()
	if err != nil {
		panic(err)
	}
	config := map[string]any{
		"Admin": map[string]any{"Pipe": `\\.\pipe\` + pipe},
		"System": map[string]any{
			"Type": "native",
			"Windows": map[string]any{
				"AdapterNamePrefix": prefix, "OperationTimeout": "15s", "Recovery": "inspect",
			},
		},
		"CommandRunner": map[string]any{"Commands": map[string]any{
			"record-marker": map[string]any{
				"Command": []string{helper, "-test.run=^TestWindowsDaemonCommandHelper$"},
				"Env":     map[string]string{commandMarkerEnvironment: marker},
				"Sync":    true, "Timeout": int64(5 * time.Second), "AllowNodes": true,
			},
		}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func triggerCommandNode(t *testing.T, daemon *windowsDaemon, marker string) {
	t.Helper()
	beforeData, _ := os.ReadFile(marker)
	before := bytes.Count(beforeData, []byte("windows-command"))
	graph := struct {
		Nodes map[string]struct {
			ID uint64 `json:"ID"`
		} `json:"nodes"`
	}{}
	decodeJSON(t, daemon.cliJSON("graph", "map"), &graph)
	node, ok := graph.Nodes["Fixture Command"]
	if !ok || node.ID == 0 {
		t.Fatalf("command node is absent from graph: %#v", graph.Nodes)
	}
	payload := fmt.Sprintf(`{"workspace":"main","id":%d}`, node.ID)
	daemon.cliJSON("raw", "--surface", "engine", "--method", "trigger_node", "--payload", payload)
	waitFor(t, "protected command marker", func() bool {
		data, err := os.ReadFile(marker)
		return err == nil && bytes.Count(data, []byte("windows-command")) > before
	})
}

func wantSOCKSHTTP(t *testing.T, socksPort, httpPort int, body string) {
	t.Helper()
	waitFor(t, "SOCKS HTTP data path", func() bool {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", socksPort), time.Second)
		if err != nil {
			return false
		}
		defer conn.Close() //nolint:errcheck
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
			return false
		}
		response := make([]byte, 2)
		if _, err = io.ReadFull(conn, response); err != nil || !bytes.Equal(response, []byte{5, 0}) {
			return false
		}
		request := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 0}
		binary.BigEndian.PutUint16(request[8:], uint16(httpPort))
		if _, err = conn.Write(request); err != nil {
			return false
		}
		reply := make([]byte, 10)
		if _, err = io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
			return false
		}
		if _, err = fmt.Fprintf(conn, "GET /marker HTTP/1.0\r\nHost: fixture.test\r\n\r\n"); err != nil {
			return false
		}
		data, err := io.ReadAll(conn)
		return err == nil && bytes.Contains(data, []byte(body))
	})
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitFor(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(integrationTimeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}

func waitCommand(command *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = command.Process.Kill()
		<-done
		return errors.New("process timeout")
	}
}

func decodeJSON(t *testing.T, data []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode JSON %q: %v", data, err)
	}
}

func isLocalSystem(t *testing.T) bool {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}

func newPrivateWindowsTestDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if isLocalSystem(t) {
		var err error
		base, err = windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(base, fmt.Sprintf("almagest-native-e2e-%d-%d", os.Getpid(), time.Now().UnixNano()))
	attributes := privateSecurityAttributes(t, true)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.CreateDirectory(name, attributes); err != nil {
		t.Fatalf("create private integration directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove integration directory: %v", err)
		}
	})
	return path
}

func writePrivateWindowsFile(t *testing.T, path string, data []byte) {
	t.Helper()
	attributes := privateSecurityAttributes(t, false)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, attributes, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("create private file %s: %v", path, err)
	}
	file := os.NewFile(uintptr(handle), path)
	if _, err = file.Write(data); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		t.Fatalf("write private file %s: %v", path, err)
	}
}

func privateSecurityAttributes(t *testing.T, directory bool) *windows.SecurityAttributes {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"O:%sD:P(A;%s;FA;;;SY)(A;%s;FA;;;BA)(A;%s;FA;;;%s)", sid, inheritance, inheritance, inheritance, sid))
	if err != nil {
		t.Fatal(err)
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
}

func ownedNetworkState(t *testing.T, prefix string) []string {
	t.Helper()
	script := fmt.Sprintf(`$items=@(); Get-NetAdapter -IncludeHidden | Where-Object Name -Like '%s*' | ForEach-Object { $i=$_.ifIndex; $items += 'adapter:'+$_.Name+':'+$_.InterfaceGuid; Get-NetRoute -InterfaceIndex $i -ErrorAction SilentlyContinue | ForEach-Object { $items += 'route:'+$_.DestinationPrefix+':'+$_.NextHop }; Get-DnsClientServerAddress -InterfaceIndex $i -ErrorAction SilentlyContinue | ForEach-Object { $items += 'dns:'+($_.ServerAddresses -join ',') } }; $items | Sort-Object -Unique`, strings.ReplaceAll(prefix, "'", "''"))
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	data, err := command.Output()
	if err != nil {
		t.Fatalf("inventory owned Windows network state: %v", err)
	}
	var state []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			state = append(state, line)
		}
	}
	slices.Sort(state)
	return state
}
