//go:build windows && winintegration

package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"golang.org/x/sys/windows"
)

// These cases port Batch 2 from WINVM_E2E_PORTING_PLAN.md. The external
// services are Go fixtures. curl.exe stays in the tests where the operating
// system client is part of the behavior under test.

const batch2CommandOutputEnvironment = "ALMAGEST_BATCH2_COMMAND_OUTPUT"

func TestWindowsBatch2AdminAPITransports(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	pipe := batch2Pipe("transports")
	webPort := reserveTCPPort(t)
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	d := newWindowsDaemon(t, root, pipe, `{}`, batch2Protected(pipe, nil))
	d.extraArgs = []string{"--web", strconv.Itoa(webPort), "--web-pprof", "--web-filter-user", current.Username}
	d.start()
	defer d.stop()

	hello := d.cliJSON("admin", "hello", "--client-name", "pipe-batch2", "--version", "e2e", "--surfaces", "admin,engine")
	if !bytes.Contains(hello, []byte("acquire_mutation")) || !bytes.Contains(hello, []byte("workspace_snapshot")) {
		t.Fatalf("named-pipe discovery is incomplete: %s", hello)
	}
	if colors := d.cliJSON("admin", "colormap"); bytes.Equal(bytes.TrimSpace(colors), []byte("{}")) {
		t.Fatal("admin color map is empty")
	}
	webURL := fmt.Sprintf("web://127.0.0.1:%d", webPort)
	wantCLIContains(t, d.cli, webURL, []string{"admin", "ping", "--nonce", "web-transport"}, "web-transport")
	wantHTTPContains(t, fmt.Sprintf("http://127.0.0.1:%d/", webPort), "<html")
	wantHTTPContains(t, fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", webPort), "Types of profiles available")

	owner := decodeCLI[api.AcquireMutationResponse](t, d.cliJSON("admin", "acquire-mutation", "--holder", "pipe-owner", "--ttl-ms", "5000"))
	if !owner.Acquired || owner.Lease == nil {
		t.Fatalf("named-pipe client did not acquire mutation lease: %#v", owner)
	}
	contender := decodeCLI[api.AcquireMutationResponse](t, runCLIJSON(t, d.cli, webURL, "admin", "acquire-mutation", "--holder", "web-contender", "--ttl-ms", "5000"))
	if contender.Acquired || contender.Active == nil || contender.Active.ID != owner.Lease.ID {
		t.Fatalf("web client did not observe named-pipe lease: %#v", contender)
	}
	runCLIJSON(t, d.cli, webURL, "admin", "renew-mutation", "--lease-id", owner.Lease.ID, "--ttl-ms", "10000")
	runCLIJSON(t, d.cli, webURL, "admin", "release-mutation", "--lease-id", owner.Lease.ID)
	expiring := decodeCLI[api.AcquireMutationResponse](t, d.cliJSON("admin", "acquire-mutation", "--holder", "expiring", "--ttl-ms", "1000"))
	if !expiring.Acquired {
		t.Fatal("short mutation lease was not acquired")
	}
	waitFor(t, "expired mutation lease takeover", func() bool {
		output, runErr := runCLIErr(d.cli, webURL, "admin", "acquire-mutation", "--holder", "successor", "--steal-expired", "--ttl-ms", "5000")
		return runErr == nil && bytes.Contains(output, []byte(`"acquired": true`))
	})
}

func TestWindowsBatch2CommandNodeLifecycle(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	pipe := batch2Pipe("command")
	marker := filepath.Join(root, "command.log")
	config := readBatch1Fixture(t, "command_node_lifecycle", "alm.hujson")
	protected := batch2CommandProtected(t, pipe, marker)
	d := newWindowsDaemon(t, root, pipe, config, protected)
	d.start()
	defer d.stop()
	assertGraphNodes(t, d, "Command", "Command Trigger", "Message", "Optional", "Other")
	wantLeftPorts(t, d, "Command", "Trigger", "slot message value", "slot optional value")
	triggerNamedNode(t, d, "Command Trigger")
	waitFileContains(t, marker, "alpha|hello|extra")

	cycleLink(t, d, "Message", "output", "Command", "slot message value")
	triggerNamedNode(t, d, "Command")
	waitFileLineCount(t, marker, 2)
	d.cliJSON("graph", "apply-menu", "--name", "Command", "--json", `{"config":{"command":"record-beta"}}`)
	wantLeftPorts(t, d, "Command", "Trigger", "slot other value")
	linkNamedPorts(t, d, "Other", "right", "output", "Command", "left", "slot other value")
	triggerNamedNode(t, d, "Command Trigger")
	waitFileContains(t, marker, "beta|second")

	d.cliJSON("graph", "apply-menu", "--name", "Command", "--json", `{"config":{"command":"record-alpha"}}`)
	linkNamedPorts(t, d, "Message", "right", "output", "Command", "left", "slot message value")
	linkNamedPorts(t, d, "Optional", "right", "output", "Command", "left", "slot optional value")
	d.stop()
	d.start()
	triggerNamedNode(t, d, "Command Trigger")
	waitFileLineCount(t, marker, 4)
}

func TestWindowsBatch2CommandHelper(t *testing.T) {
	output := os.Getenv(batch2CommandOutputEnvironment)
	if output == "" {
		return
	}
	separator := -1
	for index, value := range os.Args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		t.Fatal("command helper has no argument separator")
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := fmt.Fprintln(file, strings.Join(os.Args[separator+1:], "|"))
	err = errors.Join(writeErr, file.Close())
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowsBatch2DaemonStartupValidation(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	daemon, _ := buildWindowsBinaries(t, root)
	config := filepath.Join(root, "alm.hujson")
	protected := filepath.Join(root, "protected.hujson")
	writePrivateWindowsFile(t, config, []byte(`{}`))
	writePrivateWindowsFile(t, protected, []byte(`{"System":{"Type":"debug"}}`))
	storageFile := filepath.Join(root, "storage-file")
	writePrivateWindowsFile(t, storageFile, []byte("not a directory"))
	badConfig := filepath.Join(root, "bad.hujson")
	writePrivateWindowsFile(t, badConfig, []byte("{"))

	expectBatch2StartupFailure(t, daemon, nil, nil, "--stdin and --snapshot -", "--stdin", "--snapshot", "-", "--tmp-file-storage", "--web", "0")
	expectBatch2StartupFailure(t, daemon, nil, nil, "unknown argument", "--not-an-almagest-option")
	expectBatch2StartupFailure(t, daemon, nil, nil, "config parsing", "--config", badConfig, "--protected", protected, "--unsafe-config", "--tmp-file-storage", "--web", "0")
	expectBatch2StartupFailure(t, daemon, nil, nil, "expected a directory", "--config", config, "--protected", protected, "--unsafe-config", "--file-storage", storageFile, "--web", "0")
	expectBatch2StartupFailure(t, daemon, nil, nil, "open startup snapshot archive", "--config", config, "--protected", protected, "--unsafe-config", "--tmp-file-storage", "--snapshot", filepath.Join(root, "missing.zip"), "--web", "0")
	expectBatch2StartupFailure(t, daemon, nil, nil, "admin pipe", "--config", config, "--protected", protected, "--unsafe-config", "--tmp-file-storage", "--pipe", `\\.\pipe\invalid/name`, "--web", "0")

	runtimePipe := batch2Pipe("startup-runtime")
	runtimeConfig := `{"nodes":{"Native Tun":{"Class":"almagest/Tun","tunAddrs":"198.18.239.1/32","tunRoutes":"198.19.239.0/24","mtu":1380}}}`
	runtimeDaemon := newWindowsDaemon(t, root, runtimePipe, runtimeConfig, nativeProtectedConfig(runtimePipe, "almb2start"))
	runtimeDaemon.environment = []string{`SYSNET_WINDOWS_WINTUN_DLL=C:\missing\wintun.dll`}
	runtimeDaemon.start()
	waitBatch2NodeLabel(t, runtimeDaemon, "Native Tun", "error")
	waitFor(t, "Wintun runtime dependency error", func() bool {
		return strings.Contains(strings.ToLower(runtimeDaemon.readLog()), "wintun")
	})
	runtimeDaemon.stop()

	pipe := batch2Pipe("startup-final")
	d := newWindowsDaemon(t, root, pipe, `{}`, batch2Protected(pipe, nil))
	d.start()
	d.cliJSON("admin", "ping", "--nonce", "startup-final")
	d.stop()
}

func TestWindowsBatch2DNSClientRealDNS(t *testing.T) {
	answer := net.IPv4(192, 0, 2, 77)
	caPEM, certPEM, keyPEM := makeBatch2Certificate(t, "Batch 2 DNS Root", []string{"dot-upstream.e2e.test"}, false, "ecdsa")
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	dnsPorts := reserveBatch2UniquePorts(t, 3)
	udpPort, tcpPort, dotPort := dnsPorts[0], dnsPorts[1], dnsPorts[2]
	stopUDP := startBatch2DNSServer(t, "udp", "127.0.0.1", udpPort, nil, answer, false)
	defer stopUDP()
	stopTCP := startBatch2DNSServer(t, "tcp", "127.0.0.1", tcpPort, nil, answer, false)
	defer stopTCP()
	stopDoT := startBatch2DNSServer(t, "tcp", "127.0.0.1", dotPort, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}, answer, false)
	defer stopDoT()

	config := fmt.Sprintf(`{"nodes":{
		"Out":{"Class":"almagest/OutNetwork"},
		"Bootstrap":{"Class":"almagest/StaticDns","records":"dot-upstream.e2e.test A 127.0.0.1","ttl":60},
		"UDP":{"Class":"almagest/DnsClient","Links":["Network -> [Out] Network"],"servers":["udp://127.0.0.1:%d"]},
		"TCP":{"Class":"almagest/DnsClient","Links":["Network -> [Out] Network"],"servers":["tcp://127.0.0.1:%d"]},
		"DoT":{"Class":"almagest/DnsClient","Links":["Network -> [Out] Network","Bootstrap DNS -> [Bootstrap] DNS"],"tlsConfig":"dns-policy","servers":["dot://dot-upstream.e2e.test:%d"]},
		"UDP Query":{"Class":"almagest/DnsQuery","Links":["DNS -> [UDP] DNS"],"name":"udp.e2e.test.","type":"A"},
		"TCP Query":{"Class":"almagest/DnsQuery","Links":["DNS -> [TCP] DNS"],"name":"tcp.e2e.test.","type":"A"},
		"DoT Query":{"Class":"almagest/DnsQuery","Links":["DNS -> [DoT] DNS"],"name":"dot.e2e.test.","type":"A"}}}`, udpPort, tcpPort, dotPort)
	pipe := batch2Pipe("dns")
	protected := batch2Protected(pipe, map[string]any{
		"System": map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
		"TLS":    batch2TLSProtected("dns-root", caPEM, "dns-policy"),
	})
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, protected)
	d.start()
	defer d.stop()
	for _, node := range []string{"UDP Query", "TCP Query", "DoT Query"} {
		clickAndWait(t, d, node, "request", "send", "192.0.2.77")
	}
}

func TestWindowsBatch2FetchAddrLifecycle(t *testing.T) {
	caPEM, certPEM, keyPEM := makeBatch2Certificate(t, "Batch 2 Fetch Root", []string{"ifconfig.co", "api.ipify.org"}, false, "ecdsa")
	server := startBatch2TLSServerAt(t, "127.0.0.1:443", certPEM, keyPEM, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Host == "api.ipify.org" {
			_, _ = io.WriteString(response, `{"ip":"2001:db8::55"}`)
			return
		}
		_, _ = io.WriteString(response, `{"ip":"2001:db8::42","country_iso":"GE","city":"Tbilisi","latitude":41.7151,"longitude":44.8271}`)
	}))
	defer server()
	pipe := batch2Pipe("fetch")
	config := `{"nodes":{
		"Out":{"Class":"almagest/OutNetwork"},
		"Bootstrap":{"Class":"almagest/StaticDns","records":"ifconfig.co A 127.0.0.1\napi.ipify.org A 127.0.0.1","ttl":60},
		"Resolved Network":{"Class":"almagest/DnsInject","Links":["Network -> [Out] Network","DNS -> [Bootstrap] DNS"]},
		"Fetch Address":{"Class":"almagest/FetchAddr","Links":["Network -> [Resolved Network] Network"],"provider":"ifconfig.co","tlsConfig":"fetch-policy"}}}`
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config,
		batch2Protected(pipe, map[string]any{
			"System": map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
			"TLS":    batch2TLSProtected("fetch-root", caPEM, "fetch-policy"),
		}))
	d.start()
	defer d.stop()
	clickAndWait(t, d, "Fetch Address", "check", "check", "2001:db8::42")
	setBatch2FetchField(t, d, "provider", "ipify")
	clickAndWait(t, d, "Fetch Address", "check", "check", "2001:db8::55")
	setBatch2FetchField(t, d, "provider", "ifconfig.co")
	setBatch2FetchField(t, d, "tlsConfig", "missing-policy")
	clickAndWait(t, d, "Fetch Address", "check", "check", "error")
	cycleLink(t, d, "Fetch Address", "Network", "Resolved Network", "Network")
	setBatch2FetchField(t, d, "tlsConfig", "fetch-policy")
	clickAndWait(t, d, "Fetch Address", "check", "check", "2001:db8::42")
}

func TestWindowsBatch2LocalProxyMockIsolation(t *testing.T) {
	outsidePort := reserveTCPPort(t)
	stopTarget := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", outsidePort), "direct-outside-mock")
	defer stopTarget()
	ports := reservePorts(t, 1081, 8091)
	d := runBatch1GraphFixture(t, "local_proxy_mock_isolation", ports, "LocalProxy 1202", "HttpServer 1201", "MockNetwork 1203")
	defer d.stop()
	wantSOCKSHTTP(t, ports[1081], ports[8091], "Debug http server response")
	if err := batch2SOCKSHTTP(ports[1081], "127.0.0.1", outsidePort, "", ""); err == nil {
		t.Fatal("MockNetwork proxy reached an operating-system HTTP target")
	}
	wantHTTPContains(t, fmt.Sprintf("http://127.0.0.1:%d/", outsidePort), "direct-outside-mock")
}

func TestWindowsBatch2LocalProxyOutHTTP(t *testing.T) {
	ports := reserveBatch2UniquePorts(t, 2)
	targetPort, socksPort := ports[0], ports[1]
	stopTarget := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", targetPort), "out-network-target")
	defer stopTarget()
	config := rewriteBatch1Ports(t, readBatch1Fixture(t, "local_proxy_out_http", "alm.hujson"), map[int]int{1081: socksPort})
	pipe := batch2Pipe("proxy-out")
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, batch2Protected(pipe, map[string]any{
		"System": map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
	}))
	d.start()
	defer d.stop()
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://127.0.0.1:%d/out", targetPort), "", "", "out-network-target", false)
}

func TestWindowsBatch2NetworkFirewallCurl(t *testing.T) {
	ports := reserveBatch2UniquePorts(t, 3)
	firstPort, secondPort, socksPort := ports[0], ports[1], ports[2]
	stopFirst := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", firstPort), "firewall-one")
	defer stopFirst()
	stopSecond := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", secondPort), "firewall-two")
	defer stopSecond()
	config := rewriteBatch1Ports(t, readBatch1Fixture(t, "network_firewall_curl", "alm.hujson"), map[int]int{1191: socksPort})
	pipe := batch2Pipe("firewall")
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, batch2Protected(pipe, map[string]any{
		"System": map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
	}))
	d.start()
	defer d.stop()
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://127.0.0.1:%d/initial", firstPort), "", "", "firewall-one", false)
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://127.0.0.1:%d/initial", secondPort), "", "", "firewall-two", false)
	rule := fmt.Sprintf(`{"exclude":[{"network":"tcp","ports":[%d]}]}`, firstPort)
	d.cliJSON("graph", "apply-menu", "--name", "Firewall object policy", "--json", fmt.Sprintf(`{"state":{"value":%q}}`, rule))
	waitBatch2NodeLabel(t, d, "Network Firewall curl", "filtering 1 rules")
	if err := batch2CurlSOCKS(socksPort, fmt.Sprintf("http://127.0.0.1:%d/blocked", firstPort), "", "", false); err == nil {
		t.Fatal("NetworkFirewall object rule did not block curl.exe")
	}
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://127.0.0.1:%d/allowed", secondPort), "", "", "firewall-two", false)
	d.cliJSON("graph", "apply-menu", "--name", "Firewall object policy", "--json", `{"state":{"value":"{}"}}`)
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://127.0.0.1:%d/restored", firstPort), "", "", "firewall-one", false)
	restartAndAssert(t, d, "Network Firewall curl", "Firewall object policy")
}

func TestWindowsBatch2SafeConfigStartup(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	configDir := filepath.Join(root, "configuration")
	configPath := filepath.Join(configDir, "alm.hujson")
	storage := filepath.Join(root, "storage")
	pipe := batch2Pipe("safe-config")
	daemon, cli := buildWindowsBinaries(t, root)
	command := exec.Command(daemon, "--no-isolation", "--config", configPath, "--file-storage", storage, "--pipe", `\\.\pipe\`+pipe, "--web", "0", "--debug-nodes")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	log := &bytes.Buffer{}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	endpoint := batch2PipeEndpoint(t, pipe)
	waitFor(t, "safe-config daemon", func() bool {
		_, runErr := runCLIErr(cli, endpoint, "admin", "ping", "--nonce", "safe")
		return runErr == nil
	})
	for _, path := range []string{configDir, configPath, filepath.Join(configDir, "protected_alm.hujson"), storage} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("safe startup did not create %s: %v\n%s", path, err, log)
		}
		assertBatch2PrivateDACL(t, path)
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(command.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	if err := waitCommand(command, integrationTimeout); err != nil {
		t.Fatalf("safe-config daemon shutdown: %v\n%s", err, log)
	}
	stopped = true
}

func TestWindowsBatch2SnapshotArchiveValidation(t *testing.T) {
	pipe := batch2Pipe("archive")
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, `{}`, batch2Protected(pipe, nil))
	d.start()
	defer d.stop()
	created := decodeCLI[api.SnapshotInfo](t, d.cliJSON("snapshots", "create", "--name", "source"))
	exported := decodeCLI[api.SnapshotsExportResponse](t, d.raw(rawCall{api.SurfaceSnapshots, string(api.MethodSnapshotsExport), fmt.Sprintf(`{"uuid":%q}`, created.UUID)}))
	if len(exported.Archive) == 0 {
		t.Fatal("snapshot export returned an empty archive")
	}
	aliases := []string{
		`C:/absolute`, `C:\absolute`, `//server/share`, `files/CON`, `files/NUL.txt`,
		`files/name.`, `files/name `, `files/name:stream`, `files/a/../b`, `files\backslash`,
	}
	for _, name := range aliases {
		archive := addBatch2ZipEntry(t, exported.Archive, name, []byte("unsafe"), 0)
		path := filepath.Join(d.root, fmt.Sprintf("invalid-%d.zip", time.Now().UnixNano()))
		writePrivateWindowsFile(t, path, archive)
		d.cliFails("snapshots", "import", path)
	}
	archive := addBatch2ZipEntry(t, exported.Archive, "files/reparse", []byte("target"), uint32(syscall.S_IFLNK|0o777)<<16)
	path := filepath.Join(d.root, "reparse.zip")
	writePrivateWindowsFile(t, path, archive)
	d.cliFails("snapshots", "import", path)
	d.cliJSON("admin", "ping", "--nonce", "archive-responsive")
}

func TestWindowsBatch2SnapshotsRestore(t *testing.T) {
	pipe := batch2Pipe("restore")
	config := `{"nodes":{"Persistent Mock":{"Class":"almagest/MockNetwork"}}}`
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, batch2Protected(pipe, map[string]any{
		"Secrets": map[string]any{"protected-marker": map[string]any{"Value": "protected", "Nodes": []string{"Persistent Mock"}}},
	}))
	d.start()
	defer d.stop()
	d.raw(rawCall{api.SurfaceKV, "set", `{"path":[],"key":"snapshot-key","value":{"data":"before","string":true}}`})
	d.raw(rawCall{api.SurfaceSecrets, "set_value", `{"key":"snapshot-secret","value":"before-secret"}`})
	dataPath := filepath.Join(d.storage, "arbitrary", "nested", "value.txt")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateWindowsFile(t, dataPath, []byte("before-file"))
	builtin := decodeRaw[api.TLSBuiltinCAInfo](t, d.raw(rawCall{api.SurfaceTLS, string(api.MethodTLSBuiltinCA), `{}`}))
	created := decodeRaw[api.SnapshotInfo](t, d.raw(rawCall{api.SurfaceSnapshots, string(api.MethodSnapshotsCreate), `{"name":"complex daemon"}`}))
	exported := decodeRaw[api.SnapshotsExportResponse](t, d.raw(rawCall{api.SurfaceSnapshots, string(api.MethodSnapshotsExport), fmt.Sprintf(`{"uuid":%q}`, created.UUID)}))
	d.raw(rawCall{api.SurfaceKV, "set", `{"path":[],"key":"snapshot-key","value":{"data":"after","string":true}}`})
	d.raw(rawCall{api.SurfaceSecrets, "set_value", `{"key":"snapshot-secret","value":"after-secret"}`})
	writePrivateWindowsFile(t, dataPath, []byte("after-file"))
	mutated := filepath.Join(d.root, "mutated.hujson")
	writePrivateWindowsFile(t, mutated, []byte(`{"nodes":{"Mutated":{"Class":"almagest/MockNetwork"}}}`))
	d.cliJSON("config", "apply", mutated)
	d.raw(rawCall{api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), fmt.Sprintf(`{"uuid":%q}`, created.UUID)})
	assertGraphNodes(t, d, "Persistent Mock")
	if bytes.Contains(d.cliJSON("graph", "map"), []byte("Mutated")) {
		t.Fatal("snapshot restore retained the mutated graph")
	}
	if got := d.raw(rawCall{api.SurfaceKV, "value_snapshot", `{"path":[],"key":"snapshot-key"}`}); !bytes.Contains(got, []byte("before")) {
		t.Fatalf("snapshot did not restore bbolt KV state: %s", got)
	}
	if got := d.raw(rawCall{api.SurfaceSecrets, "snapshot", `{}`}); !bytes.Contains(got, []byte("before-secret")) {
		t.Fatalf("snapshot did not restore secret state: %s", got)
	}
	if data, err := os.ReadFile(dataPath); err != nil || string(data) != "before-file" {
		t.Fatalf("snapshot did not restore arbitrary storage: %q, %v", data, err)
	}
	afterCA := decodeRaw[api.TLSBuiltinCAInfo](t, d.raw(rawCall{api.SurfaceTLS, string(api.MethodTLSBuiltinCA), `{}`}))
	if afterCA.Cert.Fingerprint != builtin.Cert.Fingerprint {
		t.Fatal("snapshot did not restore the built-in TLS CA")
	}
	startupArchive := filepath.Join(d.root, "startup.zip")
	writePrivateWindowsFile(t, startupArchive, exported.Archive)
	d.stop()
	d.extraArgs = []string{"--snapshot", startupArchive}
	d.start()
	assertGraphNodes(t, d, "Persistent Mock")
}

func TestWindowsBatch2SnifferCurlHTTPTLS(t *testing.T) {
	reserved := reserveBatch2UniquePorts(t, 4)
	alphaPort, betaPort, tlsPort, socksPort := reserved[0], reserved[1], reserved[2], reserved[3]
	stopAlpha := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", alphaPort), "alpha body")
	defer stopAlpha()
	stopBeta := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", betaPort), "beta body")
	defer stopBeta()
	_, certPEM, keyPEM := makeBatch2Certificate(t, "tls-service.test", []string{"tls-service.test"}, true, "ecdsa")
	stopTLS := startBatch2TLSServerAt(t, fmt.Sprintf("127.0.0.1:%d", tlsPort), certPEM, keyPEM, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "tls body")
	}))
	defer stopTLS()
	ports := map[int]int{1081: socksPort, 18081: alphaPort, 18082: betaPort, 18443: tlsPort}
	config := rewriteBatch1Ports(t, readBatch1Fixture(t, "sniffer_curl_http_tls", "alm.hujson"), ports)
	pipe := batch2Pipe("sniffer")
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, batch2Protected(pipe, map[string]any{
		"System": map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
	}))
	d.start()
	defer d.stop()
	wantCurlSOCKS(t, socksPort, "http://service.test/http-alpha", "", "", "alpha body", false)
	wantCurlSOCKS(t, socksPort, "http://service.test/http-beta", "", "", "beta body", false)
	wantCurlSOCKS(t, socksPort, "https://tls-service.test/secure", "", "", "tls body", true)
	if err := batch2CurlSOCKS(socksPort, "http://service.test/rejected", "", "", false); err == nil {
		t.Fatal("Sniffer accepted an unmatched HTTP route")
	}
	cycleLink(t, d, "Sniffer 2002", "slot 1", "OutNetwork 2003", "Network")
	wantCurlSOCKS(t, socksPort, "http://service.test/http-alpha", "", "", "alpha body", false)
}

func TestWindowsBatch2SocksServerSecretCurl(t *testing.T) {
	ports := reserveBatch2UniquePorts(t, 2)
	targetPort, socksPort := ports[0], ports[1]
	stopTarget := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", targetPort), "authenticated socks")
	defer stopTarget()
	config := rewriteBatch1Ports(t, readBatch1Fixture(t, "socks_server_secret_curl", "alm.hujson"), map[int]int{11097: socksPort})
	pipe := batch2Pipe("socks-secret")
	protected := batch2Protected(pipe, map[string]any{
		"System":  map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
		"Secrets": map[string]any{"almagest-socks-password": map[string]any{"Value": "curl-socks-secret", "Nodes": []string{"SOCKS Server Secret"}}},
	})
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, protected)
	d.start()
	defer d.stop()
	url := fmt.Sprintf("http://127.0.0.1:%d/secret", targetPort)
	if err := batch2CurlSOCKS(socksPort, url, "alice", "wrong-inline-password", false); err == nil {
		t.Fatal("SOCKS server accepted the inline password instead of its protected secret")
	}
	wantCurlSOCKS(t, socksPort, url, "alice", "curl-socks-secret", "authenticated socks", false)
	d.cliJSON("secrets", "set", "--key", "runtime-password", "--value", "rotated-secret")
	d.cliJSON("secrets", "set-nodes", "--key", "runtime-password", "--node", "SOCKS Server Secret")
	d.cliJSON("graph", "apply-menu", "--name", "SOCKS Server Secret", "--json", fmt.Sprintf(`{"config":{"socksPort":%d,"users":[{"id":"alice","username":"alice","password":"fallback","passwordSecret":"runtime-password"}]}}`, socksPort))
	wantCurlSOCKS(t, socksPort, url, "alice", "rotated-secret", "authenticated socks", false)
}

func TestWindowsBatch2StdinConfigStartup(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	pipe := batch2Pipe("stdin")
	daemon, cli := buildWindowsBinaries(t, root)
	stdin := fmt.Sprintf(`{"Protected":{"Admin":{"Pipe":%q},"System":{"Type":"debug"},"Secrets":{"stdin-secret":{"Value":"protected"}}},"nodes":{"Standard Input Node":{"Class":"pasta/StringConstant","value":"stdin"}}}`, `\\.\pipe\`+pipe)
	command := exec.Command(daemon, "--no-isolation", "--stdin", "--tmp-file-storage", "--web", "0", "--debug-nodes")
	command.Stdin = strings.NewReader(stdin)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	log := &bytes.Buffer{}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	endpoint := batch2PipeEndpoint(t, pipe)
	waitFor(t, "stdin named-pipe admin", func() bool {
		_, err := runCLIErr(cli, endpoint, "admin", "ping", "--nonce", "stdin-startup")
		return err == nil
	})
	if output := runCLIJSON(t, cli, endpoint, "config", "snapshot"); !bytes.Contains(output, []byte("Standard Input Node")) || !bytes.Contains(output, []byte(`"read_only": true`)) {
		t.Fatalf("stdin config is not exposed as read-only: %s", output)
	}
	if output := runCLIJSON(t, cli, endpoint, "secrets", "list"); !bytes.Contains(output, []byte("stdin-secret")) || !bytes.Contains(output, []byte(`"value": "protected"`)) || !bytes.Contains(output, []byte(`"read_only": true`)) {
		t.Fatalf("stdin protected secret publication is invalid: %s", output)
	}
	replacement := filepath.Join(root, "replacement.hujson")
	writePrivateWindowsFile(t, replacement, []byte(`{"nodes":{}}`))
	if _, err := runCLIErr(cli, endpoint, "config", "apply", replacement); err == nil {
		t.Fatal("stdin configuration accepted a config mutation")
	}
	storage := batch2StorageFromLog(t, log.String())
	storageParent := filepath.Dir(storage)
	if filepath.Base(storageParent) != "tmp" || filepath.Base(filepath.Dir(storageParent)) != "Almagest" || !strings.HasPrefix(filepath.Base(storage), "alm-store-") {
		t.Fatalf("temporary storage %q is not identity-local Almagest temporary storage", storage)
	}
	if info, err := os.Stat(storage); err != nil || !info.IsDir() {
		t.Fatalf("temporary storage %q is not an active directory: %v", storage, err)
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(command.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	if err := waitCommand(command, integrationTimeout); err != nil {
		t.Fatalf("stdin daemon shutdown: %v\n%s", err, log)
	}
	if _, err := os.Stat(storage); !os.IsNotExist(err) {
		t.Fatalf("temporary stdin storage remained after shutdown: %v", err)
	}
	if _, err := runCLIErr(cli, endpoint, "admin", "ping", "--nonce", "stale"); err == nil {
		t.Fatal("stdin named pipe remained usable after shutdown")
	}
}

func TestWindowsBatch2TimeTriggerLifecycle(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	pipe := batch2Pipe("timer")
	intervalPath, readyPath := filepath.Join(root, "interval.log"), filepath.Join(root, "ready.log")
	config := readBatch1Fixture(t, "time_trigger_lifecycle", "alm.hujson")
	protected := batch2TimerProtected(t, pipe, intervalPath, readyPath)
	d := newWindowsDaemon(t, root, pipe, config, protected)
	d.start()
	defer d.stop()
	waitFileLineCount(t, intervalPath, 2)
	waitFileLineCount(t, readyPath, 1)
	d.cliJSON("graph", "apply-menu", "--name", "Interval Timer", "--json", `{"config":{"mode":"","interval":"80ms","dailyTime":"00:00","emitOnReady":false}}`)
	stable := waitBatch2FileLineCountStable(t, intervalPath)
	if got := waitBatch2FileLineCountStable(t, intervalPath); got != stable {
		t.Fatalf("disabled interval timer kept firing: %d -> %d", stable, got)
	}
	d.cliJSON("graph", "apply-menu", "--name", "Interval Timer", "--json", `{"config":{"mode":"interval","interval":"60ms","dailyTime":"00:00","emitOnReady":false}}`)
	waitFileLineCount(t, intervalPath, stable+2)
	cycleLink(t, d, "Interval Timer", "Trigger", "Interval Recorder", "Trigger")
	d.stop()
	d.start()
	waitFileLineCount(t, readyPath, 2)
}

func TestWindowsBatch2TLSAdminCLI(t *testing.T) {
	root := newPrivateWindowsTestDir(t)
	pipe := batch2Pipe("tls-admin")
	protectedCert, _ := makeBatch2CAKeyPair(t, "Batch 2 Protected Root", "ecdsa")
	manualCert, _ := makeBatch2CAKeyPair(t, "Batch 2 Manual Root", "rsa")
	protected := batch2Protected(pipe, map[string]any{"TLS": batch2TLSProtected("protected-ca", protectedCert, "protected-policy")})
	d := newWindowsDaemon(t, root, pipe, `{"Config":{"Save":{"IdleMilliseconds":10}}}`, protected)
	d.start()
	defer d.stop()
	initial := decodeCLI[api.TLSBuiltinCAInfo](t, d.cliJSON("tls", "builtin-ca"))
	if initial.Cert.Source != api.TLSSourceBuiltin || !initial.Cert.ReadOnly || initial.Cert.PEM == "" {
		t.Fatalf("built-in CA publication is incomplete: %#v", initial)
	}
	list := d.cliJSON("tls", "list")
	if !bytes.Contains(list, []byte("protected-ca")) || !bytes.Contains(list, []byte("protected-policy")) {
		t.Fatalf("protected TLS objects are absent: %s", list)
	}
	certPath := filepath.Join(root, "manual.pem")
	writePrivateWindowsFile(t, certPath, manualCert)
	d.cliJSON("tls", "set-cert", "--name", "manual-ca", "--pem-file", certPath)
	d.cliJSON("tls", "set-policy", "--name", "manual-policy", "--spec", `{"trust_anchors":[{"cert":"manual-ca","scope":{"any":true}}]}`)
	d.cliJSON("tls", "rename-cert", "--name", "manual-ca", "--new-name", "renamed-ca")
	list = d.cliJSON("tls", "list")
	if !bytes.Contains(list, []byte("renamed-ca")) || bytes.Contains(list, []byte(`"name": "manual-ca"`)) {
		t.Fatalf("TLS certificate rename did not update snapshot: %s", list)
	}
	changed := decodeCLI[api.TLSBuiltinCAInfo](t, d.cliJSON("tls", "set-builtin-ca", "--issuer", "Batch 2 Rotated CA", "--key-algorithm", "ed25519"))
	if changed.Cert.Fingerprint == initial.Cert.Fingerprint || changed.Options.Issuer != "Batch 2 Rotated CA" {
		t.Fatal("built-in CA rotation did not change the certificate")
	}
	for _, path := range []string{
		filepath.Join(d.storage, "tls-builtin-ca"),
		filepath.Join(d.storage, "tls-builtin-ca", "ca.pem"),
		filepath.Join(d.storage, "tls-builtin-ca", "ca-key.pem"),
	} {
		assertBatch2PrivateDACL(t, path)
	}
	d.stop()
	d.start()
	after := decodeCLI[api.TLSBuiltinCAInfo](t, d.cliJSON("tls", "builtin-ca"))
	if after.Cert.Fingerprint != changed.Cert.Fingerprint || !bytes.Contains(d.cliJSON("tls", "list"), []byte("renamed-ca")) {
		t.Fatal("TLS admin state did not persist across restart")
	}
}

func TestWindowsBatch2TLSNodesHTTPS(t *testing.T) {
	ports := reserveBatch2UniquePorts(t, 4)
	originPort, plainPort, mitmSocks, terminatorSocks := ports[0], ports[1], ports[2], ports[3]
	originCA, originCert, originKey := makeBatch2Certificate(t, "Batch 2 Origin Root", []string{"origin.test"}, false, "ecdsa")
	stopOrigin := startBatch2TLSServerAt(t, fmt.Sprintf("127.0.0.1:%d", originPort), originCert, originKey, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "mitm body")
	}))
	defer stopOrigin()
	stopPlain := startBatch2HTTPServerAt(t, fmt.Sprintf("127.0.0.1:%d", plainPort), "terminator body")
	defer stopPlain()
	config := fmt.Sprintf(`{"nodes":{
		"Out":{"Class":"almagest/OutNetwork"},
		"Names":{"Class":"almagest/StaticDns","records":"origin.test A 127.0.0.1\nterminator.test A 127.0.0.1","ttl":60},
		"Resolved":{"Class":"almagest/DnsInject","Links":["Network -> [Out] Network","DNS -> [Names] DNS"]},
		"MITM":{"Class":"almagest/TlsMitm","Links":["Network -> [Resolved] Network"],"tlsConfig":"origin-policy","filterMode":"off"},
		"MITM Proxy":{"Class":"almagest/LocalProxy","Links":["proxy -> [MITM] Network"],"socksHost":"127.0.0.1","socksPort":%d},
		"Terminator":{"Class":"almagest/TlsTerminator","Links":["Network -> [Resolved] Network"],"useSNIHostname":false,"serverAlpn":"http/1.1"},
		"Terminator Proxy":{"Class":"almagest/LocalProxy","Links":["proxy -> [Terminator] Network"],"socksHost":"127.0.0.1","socksPort":%d}}}`, mitmSocks, terminatorSocks)
	pipe := batch2Pipe("tls-nodes")
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, batch2Protected(pipe, map[string]any{
		"System": map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
		"TLS":    batch2TLSProtected("origin-ca", originCA, "origin-policy"),
	}))
	d.start()
	defer d.stop()
	ca := decodeCLI[api.TLSBuiltinCAInfo](t, d.cliJSON("tls", "builtin-ca"))
	caPath := filepath.Join(d.root, "builtin-ca.pem")
	writePrivateWindowsFile(t, caPath, []byte(ca.Cert.PEM))
	wantCurlSOCKSCA(t, mitmSocks, fmt.Sprintf("https://origin.test:%d/mitm", originPort), caPath, "mitm body")
	wantCurlSOCKSCA(t, terminatorSocks, fmt.Sprintf("https://terminator.test:%d/terminated", plainPort), caPath, "terminator body")
	cycleLink(t, d, "MITM", "Network", "Resolved", "Network")
	cycleLink(t, d, "Terminator", "Network", "Resolved", "Network")
	wantCurlSOCKSCA(t, mitmSocks, fmt.Sprintf("https://origin.test:%d/mitm", originPort), caPath, "mitm body")
	d.stop()
	d.start()
	wantCurlSOCKSCA(t, terminatorSocks, fmt.Sprintf("https://terminator.test:%d/terminated", plainPort), caPath, "terminator body")
}

func TestWindowsBatch2TLSOSReloadPrivileged(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("TLS OS reload must run in the serialized privileged WinVM suite")
	}
	root := newPrivateWindowsTestDir(t)
	installRoot := func(name string) func() {
		t.Helper()
		certPEM, _, _ := makeBatch2Certificate(t, name, nil, true, "rsa")
		certPath := filepath.Join(root, name+".pem")
		writePrivateWindowsFile(t, certPath, certPEM)
		install := exec.Command("certutil.exe", "-f", "-addstore", "Root", certPath)
		if output, err := install.CombinedOutput(); err != nil {
			t.Fatalf("install Windows test root %q: %v\n%s", name, err, output)
		}
		serial := strings.ToUpper(parseBatch2Certificate(t, certPEM).SerialNumber.Text(16))
		return func() {
			_, _ = exec.Command("certutil.exe", "-f", "-delstore", "Root", serial).CombinedOutput()
		}
	}

	removeInitial := installRoot("Batch 2 OS Reload Initial Root")
	defer removeInitial()
	pipe := batch2Pipe("os-reload")
	d := newWindowsDaemon(t, root, pipe, `{}`, batch2Protected(pipe, nil))
	d.start()
	defer d.stop()
	var hello api.HelloResponse
	decodeJSON(t, d.cliJSON("admin", "hello", "--client-name", "windows-tls-os-reload"), &hello)
	if hello.ExecutionMode != "direct" || hello.Isolation != nil {
		t.Fatalf("Windows TLS OS reload did not use direct mode: %#v", hello)
	}
	if snapshot := d.cliJSON("tls", "list"); !bytes.Contains(snapshot, []byte("Batch 2 OS Reload Initial Root")) {
		t.Fatalf("Windows certificate store root is absent at startup: %s", snapshot)
	}

	removeInitial()
	removeReplacement := installRoot("Batch 2 OS Reload Replacement Root")
	defer removeReplacement()
	reloaded := d.cliJSON("tls", "reload-os-certs")
	if bytes.Contains(reloaded, []byte("Batch 2 OS Reload Initial Root")) {
		t.Fatalf("removed Windows root remained after reload: %s", reloaded)
	}
	if !bytes.Contains(reloaded, []byte("Batch 2 OS Reload Replacement Root")) {
		t.Fatalf("replacement Windows root is absent after reload: %s", reloaded)
	}
	if !strings.Contains(d.readLog(), "TLS OS certificate reload") {
		t.Fatal("daemon did not log Windows certificate-store reload")
	}
}

func TestWindowsBatch2TLSProtectedBuiltinCA(t *testing.T) {
	algorithms := []struct {
		name string
		want string
	}{
		{"rsa", "Protected RSA Batch 2"},
		{"ecdsa", "Protected ECDSA Batch 2"},
		{"ed25519", "Protected Ed25519 Batch 2"},
	}
	for _, algorithm := range algorithms {
		t.Run(algorithm.name, func(t *testing.T) {
			cert, key := makeBatch2CAKeyPair(t, algorithm.want, algorithm.name)
			bundle := append(append([]byte(nil), cert...), key...)
			root := newPrivateWindowsTestDir(t)
			bundlePath := filepath.Join(root, "protected-ca.pem")
			writePrivateWindowsFile(t, bundlePath, bundle)
			pipe := batch2Pipe("protected-ca")
			d := newWindowsDaemon(t, root, pipe, `{}`, batch2Protected(pipe, map[string]any{
				"TLS": map[string]any{"BuiltinCA": map[string]any{"File": bundlePath}},
			}))
			d.start()
			info := decodeCLI[api.TLSBuiltinCAInfo](t, d.cliJSON("tls", "builtin-ca"))
			if !info.Immutable || !info.RegenerationDisabled || info.Options.Issuer != algorithm.want {
				t.Fatalf("protected %s CA publication is incomplete: %#v", algorithm.name, info)
			}
			d.cliFails("tls", "set-builtin-ca", "--issuer", "rejected")
			d.stop()
		})
	}
	cert, key := makeBatch2CAKeyPair(t, "Malformed Batch 2", "rsa")
	for name, bundle := range map[string][]byte{
		"missing-key":    cert,
		"missing-cert":   key,
		"duplicate-cert": append(append([]byte(nil), cert...), append(cert, key...)...),
		"trailing":       append(append(append([]byte(nil), cert...), key...), []byte("trailing")...),
	} {
		t.Run(name, func(t *testing.T) {
			root := newPrivateWindowsTestDir(t)
			path := filepath.Join(root, name+".pem")
			writePrivateWindowsFile(t, path, bundle)
			pipe := batch2Pipe("bad-protected-ca")
			d := newWindowsDaemon(t, root, pipe, `{}`, batch2Protected(pipe, map[string]any{
				"TLS": map[string]any{"BuiltinCA": map[string]any{"File": path}},
			}))
			command := d.commandForStart()
			output, err := command.CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte("protected builtin TLS CA")) {
				t.Fatalf("malformed protected CA result: %v\n%s", err, output)
			}
		})
	}
}

func TestWindowsBatch2VTunSpooferUDPRebind(t *testing.T) {
	fixtureHost := batch2FixtureIPv4(t)
	httpPort := reserveTCPPort(t)
	stopHTTP := startBatch2HTTPServerAt(t, fmt.Sprintf("%s:%d", fixtureHost, httpPort), "UDP reply-port change reached")
	defer stopHTTP()
	stopDNS := startBatch2DNSServer(t, "udp", fixtureHost, 53, nil, net.ParseIP(fixtureHost), true)
	defer stopDNS()
	config := strings.ReplaceAll(readBatch1Fixture(t, "vtun_spoofer_udp_rebind", "alm.hujson"), "%E2E_UDP_REBIND_HOST%", fixtureHost)
	config = rewriteBatch1Ports(t, config, map[int]int{18080: httpPort})
	pipe := batch2Pipe("udp-rebind")
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, batch2Protected(pipe, map[string]any{
		"System": map[string]any{"Type": "debug", "Debug": map[string]any{"NativeOutNetwork": true}},
	}))
	d.start()
	defer d.stop()
	assertGraphNodes(t, d, "UDP Rebind Client", "UDP Rebind VTun", "UDP Rebind Joiner", "UDP Rebind Spoofer", "UDP Rebind OutNetwork")
	clickAndWait(t, d, "UDP Rebind Client", "request", "send", "UDP reply-port change reached")
}

func batch2Pipe(label string) string {
	return fmt.Sprintf("almagest-b2-%s-%d-%d", label, os.Getpid(), time.Now().UnixNano())
}

func reserveBatch2UniquePorts(t *testing.T, count int) []int {
	t.Helper()
	result := make([]int, 0, count)
	for len(result) < count {
		candidate := reserveTCPPort(t)
		duplicate := false
		for _, current := range result {
			if current == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, candidate)
		}
	}
	return result
}

func batch2PipeEndpoint(t *testing.T, pipe string) string {
	t.Helper()
	if isLocalSystem(t) {
		return "pipe://machine/" + pipe
	}
	return "pipe://user/" + pipe
}

func batch2Protected(pipe string, extra map[string]any) string {
	config := map[string]any{
		"Admin":  map[string]any{"Pipe": `\\.\pipe\` + pipe},
		"System": map[string]any{"Type": "debug"},
	}
	for key, value := range extra {
		config[key] = value
	}
	data, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func batch2TLSProtected(certName string, certPEM []byte, policyName string) map[string]any {
	return map[string]any{
		"Certificates": map[string]any{certName: map[string]any{"PEM": string(certPEM)}},
		"Policies": map[string]any{policyName: map[string]any{
			"trust_anchors": []any{map[string]any{"cert": certName, "scope": map[string]any{"any": true}}},
		}},
	}
}

func batch2CommandProtected(t *testing.T, pipe, marker string) string {
	t.Helper()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(prefix string, slots ...string) map[string]any {
		argv := []string{helper, "-test.run=^TestWindowsBatch2CommandHelper$", "--", prefix}
		argv = append(argv, slots...)
		limits := map[string]any{}
		for _, slot := range slots {
			name := strings.TrimSuffix(strings.TrimPrefix(slot, "${"), "}")
			limits[name] = map[string]any{"MaxLength": 32, "Pattern": "[A-Za-z0-9_-]+", "Required": true}
		}
		return map[string]any{
			"Command": argv, "Env": map[string]string{batch2CommandOutputEnvironment: marker},
			"Sync": true, "Timeout": int64(5 * time.Second), "AllowNodes": true, "Slots": limits,
		}
	}
	return batch2Protected(pipe, map[string]any{"CommandRunner": map[string]any{"Commands": map[string]any{
		"record-alpha":   command("alpha", "${message}", "${optional}"),
		"record-beta":    command("beta", "${other}"),
		"hidden-command": map[string]any{"Command": []string{helper, "-test.run=^TestWindowsBatch2CommandHelper$"}, "Sync": true},
	}}})
}

func batch2TimerProtected(t *testing.T, pipe, interval, ready string) string {
	t.Helper()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := func(path, value string) map[string]any {
		return map[string]any{
			"Command": []string{helper, "-test.run=^TestWindowsBatch2CommandHelper$", "--", value},
			"Env":     map[string]string{batch2CommandOutputEnvironment: path}, "Sync": true,
			"Timeout": int64(5 * time.Second), "AllowNodes": true,
		}
	}
	return batch2Protected(pipe, map[string]any{"CommandRunner": map[string]any{"Commands": map[string]any{
		"record-interval": entry(interval, "interval"),
		"record-ready":    entry(ready, "ready"),
	}}})
}

func runCLIJSON(t *testing.T, cli, endpoint string, arguments ...string) []byte {
	t.Helper()
	output, err := runCLIErr(cli, endpoint, arguments...)
	if err != nil {
		t.Fatalf("CLI %q: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return output
}

func runCLIErr(cli, endpoint string, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	args := append([]string{"--url", endpoint}, arguments...)
	return exec.CommandContext(ctx, cli, args...).CombinedOutput()
}

func wantCLIContains(t *testing.T, cli, endpoint string, arguments []string, marker string) {
	t.Helper()
	output := runCLIJSON(t, cli, endpoint, arguments...)
	if !bytes.Contains(output, []byte(marker)) {
		t.Fatalf("CLI output does not contain %q: %s", marker, output)
	}
}

func wantHTTPContains(t *testing.T, url, marker string) {
	t.Helper()
	waitFor(t, "HTTP fixture "+url, func() bool {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get(url)
		if err != nil {
			return false
		}
		defer response.Body.Close() //nolint:errcheck
		data, err := io.ReadAll(response.Body)
		return err == nil && response.StatusCode == http.StatusOK && bytes.Contains(data, []byte(marker))
	})
}

func startBatch2HTTPServerAt(t *testing.T, address, body string) func() {
	t.Helper()
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("listen for HTTP fixture on %s: %v", address, err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, body)
	}), ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

func startBatch2TLSServerAt(t *testing.T, address string, certPEM, keyPEM []byte, handler http.Handler) func() {
	t.Helper()
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("listen for TLS fixture on %s: %v", address, err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = server.Serve(tlsListener) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

func makeBatch2Certificate(t *testing.T, issuer string, hosts []string, selfSigned bool, algorithm string) ([]byte, []byte, []byte) {
	t.Helper()
	if selfSigned {
		cert, key := makeBatch2SignedCertificate(t, issuer, hosts, algorithm, nil, nil)
		return cert, cert, key
	}
	caCert, caKey := makeBatch2CAKeyPair(t, issuer, algorithm)
	parsedCA := parseBatch2Certificate(t, caCert)
	privateCA := parseBatch2PrivateKey(t, caKey)
	leaf, leafKey := makeBatch2SignedCertificate(t, hosts[0], hosts, algorithm, parsedCA, privateCA)
	return caCert, leaf, leafKey
}

func makeBatch2CAKeyPair(t *testing.T, issuer, algorithm string) ([]byte, []byte) {
	t.Helper()
	return makeBatch2SignedCertificate(t, issuer, nil, algorithm, nil, nil)
}

func makeBatch2SignedCertificate(t *testing.T, commonName string, hosts []string, algorithm string, parent *x509.Certificate, parentKey crypto.PrivateKey) ([]byte, []byte) {
	t.Helper()
	key := makeBatch2PrivateKey(t, algorithm)
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 120)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true,
	}
	if len(hosts) == 0 {
		template.IsCA = true
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature
	} else {
		template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		for _, host := range hosts {
			if ip := net.ParseIP(host); ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			} else {
				template.DNSNames = append(template.DNSNames, host)
			}
		}
	}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, batch2PublicKey(key), parentKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func makeBatch2PrivateKey(t *testing.T, algorithm string) crypto.PrivateKey {
	t.Helper()
	switch algorithm {
	case "rsa":
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		return key
	case "ecdsa":
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	case "ed25519":
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	default:
		t.Fatalf("unsupported test key algorithm %q", algorithm)
		return nil
	}
}

func batch2PublicKey(key crypto.PrivateKey) crypto.PublicKey {
	switch typed := key.(type) {
	case *rsa.PrivateKey:
		return &typed.PublicKey
	case *ecdsa.PrivateKey:
		return &typed.PublicKey
	case ed25519.PrivateKey:
		return typed.Public()
	default:
		panic(fmt.Sprintf("unsupported private key %T", key))
	}
}

func parseBatch2Certificate(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("certificate PEM is absent")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func parseBatch2PrivateKey(t *testing.T, data []byte) crypto.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("private key PEM is absent")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func startBatch2DNSServer(t *testing.T, network, host string, port int, tlsConfig *tls.Config, answer net.IP, replyFromDifferentPort bool) func() {
	t.Helper()
	address := net.JoinHostPort(host, strconv.Itoa(port))
	if network == "udp" {
		listener, err := net.ListenUDP("udp4", mustBatch2UDPAddr(t, address))
		if err != nil {
			t.Fatalf("listen for UDP DNS fixture: %v", err)
		}
		reply := listener
		if replyFromDifferentPort {
			reply, err = net.ListenUDP("udp4", mustBatch2UDPAddr(t, net.JoinHostPort(host, "0")))
			if err != nil {
				_ = listener.Close()
				t.Fatal(err)
			}
			if reply.LocalAddr().(*net.UDPAddr).Port == port {
				t.Fatal("UDP rebind fixture did not allocate a different response port")
			}
		}
		go func() {
			buffer := make([]byte, 64<<10)
			for {
				length, source, readErr := listener.ReadFromUDP(buffer)
				if readErr != nil {
					return
				}
				response, responseErr := batch2DNSResponse(buffer[:length], answer)
				if responseErr == nil {
					_, _ = reply.WriteToUDP(response, source)
				}
			}
		}()
		return func() {
			_ = listener.Close()
			if reply != listener {
				_ = reply.Close()
			}
		}
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("listen for TCP DNS fixture: %v", err)
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close() //nolint:errcheck
				var length [2]byte
				if _, readErr := io.ReadFull(connection, length[:]); readErr != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(length[:]))
				if _, readErr := io.ReadFull(connection, query); readErr != nil {
					return
				}
				response, responseErr := batch2DNSResponse(query, answer)
				if responseErr != nil {
					return
				}
				binary.BigEndian.PutUint16(length[:], uint16(len(response)))
				_, _ = connection.Write(append(length[:], response...))
			}()
		}
	}()
	return func() { _ = listener.Close() }
}

func mustBatch2UDPAddr(t *testing.T, address string) *net.UDPAddr {
	t.Helper()
	result, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func batch2DNSResponse(query []byte, answer net.IP) ([]byte, error) {
	if len(query) < 12 || binary.BigEndian.Uint16(query[4:6]) != 1 {
		return nil, errors.New("invalid DNS query")
	}
	offset := 12
	for offset < len(query) && query[offset] != 0 {
		length := int(query[offset])
		offset += length + 1
	}
	questionEnd := offset + 5
	if questionEnd > len(query) {
		return nil, errors.New("truncated DNS question")
	}
	response := append([]byte(nil), query[:2]...)
	response = append(response, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0)
	response = append(response, query[12:questionEnd]...)
	response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
	response = append(response, answer.To4()...)
	return response, nil
}

func batch2SOCKSHTTP(socksPort int, host string, port int, username, password string) error {
	connection, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", socksPort), 2*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close() //nolint:errcheck
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	methods := []byte{0}
	if username != "" {
		methods = []byte{2}
	}
	if _, err = connection.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err = io.ReadFull(connection, response); err != nil {
		return err
	}
	if username != "" {
		request := append([]byte{1, byte(len(username))}, []byte(username)...)
		request = append(request, byte(len(password)))
		request = append(request, password...)
		if _, err = connection.Write(request); err != nil {
			return err
		}
		if _, err = io.ReadFull(connection, response); err != nil || response[1] != 0 {
			return fmt.Errorf("SOCKS authentication failed: %v, %w", response, err)
		}
	} else if response[1] != 0 {
		return fmt.Errorf("SOCKS no-auth method was rejected: %v", response)
	}
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, host...)
	request = append(request, byte(port>>8), byte(port))
	if _, err = connection.Write(request); err != nil {
		return err
	}
	if err = readBatch1SOCKSReply(connection); err != nil {
		return err
	}
	_, err = fmt.Fprintf(connection, "GET / HTTP/1.0\r\nHost: %s\r\n\r\n", host)
	return err
}

func batch2CurlSOCKS(socksPort int, url, username, password string, insecure bool) error {
	arguments := []string{"--fail", "--silent", "--show-error", "--connect-timeout", "2", "--max-time", "5", "--socks5-hostname", fmt.Sprintf("127.0.0.1:%d", socksPort)}
	if username != "" {
		arguments = append(arguments, "--proxy-user", username+":"+password)
	}
	if insecure {
		arguments = append(arguments, "--insecure")
	}
	arguments = append(arguments, url)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "curl.exe", arguments...).Run()
}

func wantCurlSOCKS(t *testing.T, socksPort int, url, username, password, marker string, insecure bool) {
	t.Helper()
	waitFor(t, "curl.exe through SOCKS", func() bool {
		arguments := []string{"--fail", "--silent", "--show-error", "--connect-timeout", "2", "--max-time", "5", "--socks5-hostname", fmt.Sprintf("127.0.0.1:%d", socksPort)}
		if username != "" {
			arguments = append(arguments, "--proxy-user", username+":"+password)
		}
		if insecure {
			arguments = append(arguments, "--insecure")
		}
		arguments = append(arguments, url)
		output, err := exec.Command("curl.exe", arguments...).CombinedOutput()
		return err == nil && bytes.Contains(output, []byte(marker))
	})
}

func wantCurlSOCKSCA(t *testing.T, socksPort int, url, caPath, marker string) {
	t.Helper()
	var lastOutput []byte
	var lastErr error
	deadline := time.Now().Add(integrationTimeout)
	for time.Now().Before(deadline) {
		lastOutput, lastErr = exec.Command("curl.exe", "--fail", "--silent", "--show-error", "--http1.1", "--ssl-no-revoke",
			"--connect-timeout", "2", "--max-time", "8", "--socks5-hostname", fmt.Sprintf("127.0.0.1:%d", socksPort),
			"--cacert", caPath, url).CombinedOutput()
		if lastErr == nil && bytes.Contains(lastOutput, []byte(marker)) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("curl.exe TLS path failed: %v: %s", lastErr, lastOutput)
}

func triggerNamedNode(t *testing.T, d *windowsDaemon, name string) {
	t.Helper()
	snapshot := decodeCLI[api.WorkspaceSnapshotResponse](t, d.cliJSON("engine", "workspace-snapshot")).Snapshot
	for id, node := range snapshot.Nodes {
		if node.Name == name {
			d.raw(rawCall{api.SurfaceEngine, string(api.MethodEngineTriggerNode), fmt.Sprintf(`{"workspace":"main","id":%d}`, id)})
			return
		}
	}
	t.Fatalf("cannot find node %q", name)
}

func wantLeftPorts(t *testing.T, d *windowsDaemon, name string, want ...string) {
	t.Helper()
	waitFor(t, "left ports of "+name, func() bool {
		snapshot := decodeCLI[api.WorkspaceSnapshotResponse](t, d.cliJSON("engine", "workspace-snapshot")).Snapshot
		for _, node := range snapshot.Nodes {
			if node.Name != name {
				continue
			}
			got := make([]string, 0, len(node.LeftPorts))
			for _, id := range node.LeftPorts {
				got = append(got, snapshot.Ports[id].Name)
			}
			return strings.Join(got, "\x00") == strings.Join(want, "\x00")
		}
		return false
	})
}

func waitBatch2NodeLabel(t *testing.T, d *windowsDaemon, name, label string) {
	t.Helper()
	waitFor(t, name+" label "+label, func() bool {
		snapshot := decodeCLI[api.WorkspaceSnapshotResponse](t, d.cliJSON("engine", "workspace-snapshot")).Snapshot
		for _, node := range snapshot.Nodes {
			if node.Name == name {
				return node.Label == label
			}
		}
		return false
	})
}

func linkNamedPorts(t *testing.T, d *windowsDaemon, nodeA, directionA, portA, nodeB, directionB, portB string) {
	t.Helper()
	snapshot := decodeCLI[api.WorkspaceSnapshotResponse](t, d.cliJSON("engine", "workspace-snapshot")).Snapshot
	var idA, idB uint64
	for id, node := range snapshot.Nodes {
		if node.Name == nodeA {
			idA = id
		}
		if node.Name == nodeB {
			idB = id
		}
	}
	portIDA, portIDB := namedPortID(snapshot, idA, directionA, portA), namedPortID(snapshot, idB, directionB, portB)
	if portIDA == 0 || portIDB == 0 {
		t.Fatalf("cannot link %s.%s to %s.%s", nodeA, portA, nodeB, portB)
	}
	d.cliJSON("graph", "add-link", "--port-a", strconv.FormatUint(portIDA, 10), "--port-b", strconv.FormatUint(portIDB, 10))
}

func waitFileContains(t *testing.T, path, marker string) {
	t.Helper()
	waitFor(t, "file marker "+marker, func() bool {
		data, err := os.ReadFile(path)
		return err == nil && bytes.Contains(data, []byte(marker))
	})
}

func waitFileLineCount(t *testing.T, path string, count int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d lines in %s", count, path), func() bool { return batch2FileLines(path) >= count })
}

func batch2FileLines(path string) int {
	data, _ := os.ReadFile(path)
	return bytes.Count(data, []byte("\n"))
}

func waitBatch2FileLineCountStable(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(integrationTimeout)
	previous := batch2FileLines(path)
	quietTicks := 0
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		current := batch2FileLines(path)
		if current == previous {
			quietTicks++
			if quietTicks >= 8 {
				return current
			}
			continue
		}
		previous = current
		quietTicks = 0
	}
	t.Fatalf("line count in %s did not stabilize", path)
	return 0
}

func expectBatch2StartupFailure(t *testing.T, daemon string, stdin io.Reader, environment []string, marker string, arguments ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, daemon, append([]string{"--no-isolation"}, arguments...)...)
	command.Stdin = stdin
	if len(environment) != 0 {
		command.Env = windowsTestEnvironment(os.Environ(), environment...)
	}
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("startup unexpectedly succeeded: %s", strings.Join(arguments, " "))
	}
	if !bytes.Contains(bytes.ToLower(output), bytes.ToLower([]byte(marker))) {
		t.Fatalf("startup failure does not contain %q: %v\n%s", marker, err, output)
	}
}

func setBatch2FetchField(t *testing.T, d *windowsDaemon, field string, value any) {
	t.Helper()
	var menu struct {
		MenuID string `json:"menuId"`
	}
	if err := json.Unmarshal(d.cliJSON("graph", "menu", "--name", "Fetch Address"), &menu); err != nil {
		t.Fatalf("decode FetchAddr menu: %v", err)
	}
	if menu.MenuID == "" {
		t.Fatal("FetchAddr menu has no menuId")
	}
	message, err := json.Marshal(map[string]any{
		"type":   "field.update",
		"menuId": menu.MenuID,
		"field": map[string]any{
			"blockId": "check",
			"fieldId": field,
		},
		"value": value,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.cliJSON("engine", "send-node-formular-msg", "--name", "Fetch Address", "--message", string(message))
}

func batch2StorageFromLog(t *testing.T, log string) string {
	t.Helper()
	const marker = "Using file storage at "
	for _, line := range strings.Split(log, "\n") {
		if index := strings.Index(line, marker); index >= 0 {
			path := strings.TrimSpace(line[index+len(marker):])
			if path != "" {
				return path
			}
		}
	}
	t.Fatalf("daemon did not report its temporary storage path:\n%s", log)
	return ""
}

func assertBatch2PrivateDACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read DACL for %s: %v", path, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatalf("read owner for %s: %v", path, err)
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if !owner.Equals(current.User.Sid) {
		t.Fatalf("owner for %s = %s, want %s", path, owner, current.User.Sid)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL for %s is absent: %v", path, err)
	}
}

func batch2FixtureIPv4(t *testing.T) string {
	t.Helper()
	connection, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		t.Fatalf("select fixture IPv4 address: %v", err)
	}
	defer connection.Close() //nolint:errcheck
	address := connection.LocalAddr().(*net.UDPAddr).IP
	if address == nil || address.IsLoopback() || address.To4() == nil {
		t.Fatalf("fixture IPv4 address = %v, want non-loopback IPv4", address)
	}
	return address.String()
}

func addBatch2ZipEntry(t *testing.T, archive []byte, name string, data []byte, externalAttributes uint32) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, source := range reader.File {
		header := source.FileHeader
		target, createErr := writer.CreateHeader(&header)
		if createErr != nil {
			t.Fatal(createErr)
		}
		input, openErr := source.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, copyErr := io.Copy(target, input)
		closeErr := input.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			t.Fatal(err)
		}
	}
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o600)
	if externalAttributes != 0 {
		header.CreatorVersion = (3 << 8) | 20
		header.ExternalAttrs = externalAttributes
	}
	target, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = target.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
