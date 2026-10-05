//go:build windows && winintegration

package integration_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// These tests port the single-VM part of Batch 3 from
// WINVM_E2E_PORTING_PLAN.md. They run only in the serialized privileged suite:
// each case owns native adapters, routes, DNS state, or split-driver state.

func TestWindowsBatch3DefaultTunDNSProvider(t *testing.T) {
	requireBatch3Privilege(t)
	const dnsAddress = "198.18.243.1"
	config := fmt.Sprintf(`{"nodes":{
		"DefaultTun":{"Class":"almagest/DefaultTun","Links":["DNS -> [Managed DNS] DNS"],"tunAddrs":%q,"dnsIP":%q},
		"Managed DNS":{"Class":"almagest/StaticDns","records":"batch3-dns.test A 192.0.2.43","ttl":1}
	}}`, dnsAddress+"/32", dnsAddress)
	d := startBatch3Daemon(t, "dns", config)
	waitBatch2NodeLabel(t, d, "DefaultTun", "ready")
	wantBatch3DNSAnswer(t, dnsAddress, "batch3-dns.test", net.IPv4(192, 0, 2, 43))

	d.cliJSON("graph", "apply-menu", "--name", "Managed DNS", "--json",
		`{"config":{"records":"batch3-dns.test A 192.0.2.44","ttl":1}}`)
	wantBatch3DNSAnswer(t, dnsAddress, "batch3-dns.test", net.IPv4(192, 0, 2, 44))
	cycleLink(t, d, "DefaultTun", "DNS", "Managed DNS", "DNS")
	wantBatch3DNSAnswer(t, dnsAddress, "batch3-dns.test", net.IPv4(192, 0, 2, 44))
}

func TestWindowsBatch3DefaultTunNestedWireGuardBypass(t *testing.T) {
	requireBatch3Privilege(t)
	targetAddress := batch3UnderlayIPv4(t)
	targetPort, socksPort := reserveTCPPort(t), reserveTCPPort(t)
	stopTarget := startBatch2HTTPServerAt(t, net.JoinHostPort(targetAddress, fmt.Sprint(targetPort)), "nested-wireguard-bypass")
	defer stopTarget()
	config := fmt.Sprintf(`{"nodes":{
		"DefaultTun":{"Class":"almagest/DefaultTun","tunAddrs":"198.18.244.1/32"},
		"WireGuard VTun":{"Class":"almagest/VTun","Links":["Tun -> [WireGuard] Tun"],"name":"batch3-nested-wg"},
		"WireGuard":{"Class":"almagest/WireGuard","Links":["Network -> [Out] Network"],"privateKey":"CAkKCwwNDg8QERITFBUWFxgZGhscHR4fICEiIyQlJkc=","ownedIPs":"10.244.0.1/32"},
		"Out":{"Class":"almagest/OutNetwork"},
		"Bypass Proxy":{"Class":"almagest/LocalProxy","Links":["proxy -> [Out] Network"],"socksHost":"127.0.0.1","socksPort":%d}
	}}`, socksPort)
	d := startBatch3Daemon(t, "nested-wg", config)
	waitBatch2NodeLabel(t, d, "DefaultTun", "ready")
	waitBatch2NodeLabel(t, d, "WireGuard", "up")
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://%s:%d/bypass", targetAddress, targetPort), "", "", "nested-wireguard-bypass", false)
	restartAndAssert(t, d, "DefaultTun", "WireGuard", "WireGuard VTun", "Bypass Proxy")
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://%s:%d/restarted", targetAddress, targetPort), "", "", "nested-wireguard-bypass", false)
}

func TestWindowsBatch3DefaultTunRouting(t *testing.T) {
	requireBatch3Privilege(t)
	d := startBatch3Daemon(t, "default-routing", batch3DefaultTunHTTPConfig("10.243.1.1", "10.243.1.2", 18431, "default-tun-routing"))
	waitBatch2NodeLabel(t, d, "DefaultTun", "ready")
	waitBatch2NodeLabel(t, d, "Spoofer", "forwarding")
	wantBatch3Curl(t, "http://10.243.1.2:18431/routed", "default-tun-routing")
	cycleLink(t, d, "DefaultTun", "Tun", "Spoofer", "Tun")
	wantBatch3Curl(t, "http://10.243.1.2:18431/reattached", "default-tun-routing")
}

func TestWindowsBatch3DefaultTunZeroPrefixRoutes(t *testing.T) {
	requireBatch3Privilege(t)
	config := `{"nodes":{
		"DefaultTun":{"Class":"almagest/DefaultTun","tunAddrs":"10.246.0.1/24\nfd46::1/64","tunRoutes":"0.0.0.0/0\n::/0"}
	}}`
	d := startBatch3Daemon(t, "zero-prefix", config)
	waitBatch2NodeLabel(t, d, "DefaultTun", "ready")
	menu := d.cliJSON("graph", "menu", "--name", "DefaultTun")
	if !bytes.Contains(menu, []byte("0.0.0.0/0")) || !bytes.Contains(menu, []byte("::/0")) {
		t.Fatalf("effective DefaultTun menu lost zero-prefix routes: %s", menu)
	}
	wantBatch3AdapterRouteState(t, "almb3zero", true)
	d.cliJSON("graph", "apply-menu", "--name", "DefaultTun", "--json",
		`{"config":{"tunAddrs":"10.247.0.1/24\nfd47::1/64","tunRoutes":"0.0.0.0/0\n::/0"}}`)
	waitBatch2NodeLabel(t, d, "DefaultTun", "ready")
	wantBatch3AdapterRouteState(t, "almb3zero", true)
}

func TestWindowsBatch3OutNetworkMigration(t *testing.T) {
	requireBatch3Privilege(t)
	targetAddress := batch3UnderlayIPv4(t)
	targetPort, socksPort := reserveTCPPort(t), reserveTCPPort(t)
	stopTarget := startBatch2HTTPServerAt(t, net.JoinHostPort(targetAddress, fmt.Sprint(targetPort)), "out-network-migration")
	defer stopTarget()
	config := fmt.Sprintf(`{"nodes":{
		"Out":{"Class":"almagest/OutNetwork"},
		"Proxy":{"Class":"almagest/LocalProxy","Links":["proxy -> [Out] Network"],"socksHost":"127.0.0.1","socksPort":%d}
	}}`, socksPort)
	d := startBatch3Daemon(t, "migration", config)
	for cycle := 0; cycle < 3; cycle++ {
		wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://%s:%d/cycle-%d", targetAddress, targetPort, cycle), "", "", "out-network-migration", false)
		d.stop()
		d.start()
	}
	wantCurlSOCKS(t, socksPort, fmt.Sprintf("http://%s:%d/final", targetAddress, targetPort), "", "", "out-network-migration", false)
}

func TestWindowsBatch3TunFirewallCurl(t *testing.T) {
	requireBatch3Privilege(t)
	config := strings.Replace(readBatch1Fixture(t, "tun_firewall_curl", "alm.hujson"),
		`"name":      "almfw0",`, "", 1)
	config = strings.Replace(config, `"tunRoutes": "10.213.17.0/24",`, "", 1)
	d := startBatch3Daemon(t, "tun-firewall", config)
	waitBatch2NodeLabel(t, d, "Firewall Tun", "ready")
	waitBatch2NodeLabel(t, d, "Firewall Spoofer", "forwarding")
	wantBatch3Curl(t, "http://10.213.17.2:18111/allowed", "tun firewall service one")
	wantBatch3Curl(t, "http://10.213.17.2:18112/allowed", "tun firewall service two")
	d.cliJSON("graph", "apply-menu", "--name", "TUN firewall object policy", "--json",
		`{"state":{"value":"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[18111]}]}"}}`)
	waitBatch2NodeLabel(t, d, "TUN Firewall curl", "filtering 1 rules")
	if err := batch3Curl("http://10.213.17.2:18111/blocked"); err == nil {
		t.Fatal("TunFirewall did not block curl.exe for the excluded port")
	}
	wantBatch3Curl(t, "http://10.213.17.2:18112/still-allowed", "tun firewall service two")
}

func TestWindowsBatch3TunHighLoad(t *testing.T) {
	requireBatch3Privilege(t)
	config := readBatch1Fixture(t, "tun_high_load", "alm.hujson")
	for _, name := range []string{"almload0", "almload1", "almload2"} {
		config = strings.Replace(config, `"name": "`+name+`",`, "", 1)
	}
	for _, route := range []string{"10.221.1.0/24", "10.222.1.0/24", "10.223.1.0/24"} {
		config = strings.Replace(config, `"tunRoutes": "`+route+`",`, "", 1)
	}
	d := startBatch3Daemon(t, "tun-load", config)
	for _, node := range []string{"Load direct TUN", "Load middleware TUN", "Load split TUN"} {
		waitBatch2NodeLabel(t, d, node, "ready")
	}
	targets := []struct{ url, marker string }{
		{"http://10.221.1.2:18201/", "direct-load-response"},
		{"http://10.222.1.2:18202/", "middleware-load-response"},
		{"http://10.223.1.2:18203/", "split-a-load-response"},
		{"http://10.223.1.2:18204/", "split-b-load-response"},
	}
	var group sync.WaitGroup
	errors := make(chan error, 32)
	for index := 0; index < 32; index++ {
		current := targets[index%len(targets)]
		group.Add(1)
		go func() {
			defer group.Done()
			output, err := batch3CurlOutput(current.url)
			if err != nil || !bytes.Contains(output, []byte(current.marker)) {
				errors <- fmt.Errorf("%s: %w: %s", current.url, err, output)
			}
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestWindowsBatch3TunRouting(t *testing.T) {
	requireBatch3Privilege(t)
	d := startBatch3Daemon(t, "tun-routing", readBatch1Fixture(t, "tun_routing", "alm.hujson"))
	waitBatch2NodeLabel(t, d, "Tun 1504", "tun1")
	waitBatch2NodeLabel(t, d, "Tun 1508", "tun2")
	wantBatch3Curl(t, "http://10.7.160.2:8080/first", "Server 1")
	wantBatch3Curl(t, "http://10.210.148.2:8080/second", "Server 2")
	cycleLink(t, d, "Tun 1504", "Tun", "Spoofer 1510", "Tun")
	wantBatch3Curl(t, "http://10.7.160.2:8080/restored", "Server 1")
}

func TestWindowsBatch3TunSelectMockHTTP(t *testing.T) {
	requireBatch3Privilege(t)
	d := startBatch3Daemon(t, "tun-select", readBatch1Fixture(t, "tun_select_mock_http", "alm.hujson"))
	waitBatch2NodeLabel(t, d, "Tun 1901", "tun-select-a")
	waitBatch2NodeLabel(t, d, "Tun 1902", "tun-select-b")
	wantBatch3Curl(t, "http://10.71.160.2:8080/initial", "Server A")
	wantBatch3Curl(t, "http://10.72.160.2:8081/initial", "Server B")
	d.cliJSON("graph", "apply-menu", "--name", "BoolConstant 1815", "--json", `{"state":{"value":true}}`)
	wantBatch3Curl(t, "http://10.71.160.2:8080/switched", "Server B")
	wantBatch3Curl(t, "http://10.72.160.2:8081/switched", "Server A")
}

func TestWindowsBatch3TunSplitterProcessRouting(t *testing.T) {
	requireBatch3Privilege(t)
	curlPath := strings.ReplaceAll(batch3CurlPath(t), `\`, `/`)
	config := fmt.Sprintf(`{"nodes":{
		"Tun":{"Class":"almagest/Tun","Links":["Tun -> [Splitter] Tun"],"tunAddrs":"10.245.1.1/24"},
		"Splitter":{"Class":"almagest/TunSplitter","Links":["slot 1 -> [Curl Spoofer] Tun","slot 2 -> [Fallback Spoofer] Tun"],"rules":%q},
		"Curl Spoofer":{"Class":"almagest/Spoofer","Links":["Network -> [Curl Network] Network"]},
		"Fallback Spoofer":{"Class":"almagest/Spoofer","Links":["Network -> [Fallback Network] Network"]},
		"Curl Network":{"Class":"almagest/MockNetwork"},"Fallback Network":{"Class":"almagest/MockNetwork"},
		"Curl Server":{"Class":"almagest/HttpServer","Links":["Network -> [Curl Network] Network"],"host":"10.245.1.2","port":18451,"response":"curl-process-route"},
		"Fallback Server":{"Class":"almagest/HttpServer","Links":["Network -> [Fallback Network] Network"],"host":"10.245.1.2","port":18451,"response":"fallback-process-route"}
	}}`, "RULE win-exe-path "+curlPath+"\nSLOT 1\nTRUE\nSLOT 2")
	d := startBatch3Daemon(t, "splitter", config)
	waitBatch2NodeLabel(t, d, "Tun", "ready")
	waitBatch2NodeLabel(t, d, "Splitter", "splitting")
	wantBatch3Curl(t, "http://10.245.1.2:18451/process", "curl-process-route")
}

func TestWindowsBatch3VTunSpooferIPv6Fallback(t *testing.T) {
	requireBatch3Privilege(t)
	config := `{"nodes":{
		"Client":{"Class":"almagest/HttpClient","Links":["Network -> [VTun] Network"],"url":"http://fallback.batch3.test:18461/","method":"GET"},
		"VTun":{"Class":"almagest/VTun","Links":["Tun -> [Joiner] Default"],"name":"batch3-ipv6-fallback","localAddrs":"192.0.2.2\n2001:db8:46::2","dnsServers":"192.0.2.53","noLoopbackAddr":true},
		"Joiner":{"Class":"almagest/TunJoiner","Links":["Tun -> [Spoofer] Tun"]},
		"Spoofer":{"Class":"almagest/Spoofer","Links":["Network -> [Out] Network"]},
		"Out":{"Class":"almagest/OutNetwork"}
	}}`
	d := startBatch3Daemon(t, "ipv6-fallback", config)
	waitBatch2NodeLabel(t, d, "VTun", "batch3-ipv6-fallback")
	waitBatch2NodeLabel(t, d, "Joiner", "joined default")
	waitBatch2NodeLabel(t, d, "Spoofer", "forwarding")
	menu := d.cliJSON("graph", "menu", "--name", "VTun")
	if !bytes.Contains(menu, []byte("ipv4")) || !bytes.Contains(menu, []byte("ipv6")) {
		t.Fatalf("VTun does not report both address families: %s", menu)
	}
	cycleLink(t, d, "Joiner", "Tun", "Spoofer", "Tun")
	waitBatch2NodeLabel(t, d, "Spoofer", "forwarding")
}

func requireBatch3Privilege(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() || !isLocalSystem(t) {
		t.Fatal("Batch 3 must run as Local System in the serialized privileged WinVM suite")
	}
}

func startBatch3Daemon(t *testing.T, suffix, config string) *windowsDaemon {
	t.Helper()
	pipe := batch2Pipe("batch3-" + suffix)
	prefix := "almb3" + strings.ReplaceAll(suffix, "-", "")
	if len(prefix) > 14 {
		prefix = prefix[:14]
	}
	d := newWindowsDaemon(t, newPrivateWindowsTestDir(t), pipe, config, nativeProtectedConfig(pipe, prefix))
	d.start()
	t.Cleanup(d.stop)
	return d
}

func batch3DefaultTunHTTPConfig(local, target string, port int, response string) string {
	return fmt.Sprintf(`{"nodes":{
		"DefaultTun":{"Class":"almagest/DefaultTun","Links":["Tun -> [Spoofer] Tun"],"tunAddrs":%q},
		"Spoofer":{"Class":"almagest/Spoofer","Links":["Network -> [Mock] Network"]},
		"Mock":{"Class":"almagest/MockNetwork"},
		"HTTP":{"Class":"almagest/HttpServer","Links":["Network -> [Mock] Network"],"host":%q,"port":%d,"response":%q}
	}}`, local+"/24", target, port, response)
}

func batch3CurlPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("curl.exe")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func batch3UnderlayIPv4(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || strings.HasPrefix(strings.ToLower(iface.Name), "almb3") {
			continue
		}
		addresses, addressErr := iface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr == nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	t.Fatal("no active IPv4 underlay address is available")
	return ""
}

func batch3Curl(url string) error {
	_, err := batch3CurlOutput(url)
	return err
}

func batch3CurlOutput(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "curl.exe", "--fail", "--silent", "--show-error", "--noproxy", "*",
		"--connect-timeout", "2", "--max-time", "8", url).CombinedOutput()
}

func wantBatch3Curl(t *testing.T, url, marker string) {
	t.Helper()
	waitFor(t, "curl.exe response from "+url, func() bool {
		output, err := batch3CurlOutput(url)
		return err == nil && bytes.Contains(output, []byte(marker))
	})
}

func wantBatch3AdapterRouteState(t *testing.T, prefix string, wantDefault bool) {
	t.Helper()
	script := fmt.Sprintf(`$a=@(Get-NetAdapter | Where-Object Name -Like '%s*'); if($a.Count -ne 1){exit 2}; $r=@(Get-NetRoute -InterfaceIndex $a[0].ifIndex -ErrorAction SilentlyContinue); if(@($r | Where-Object DestinationPrefix -In '0.0.0.0/0','::/0').Count -lt %d){exit 3}`, prefix, map[bool]int{true: 2, false: 0}[wantDefault])
	output, err := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect DefaultTun route state: %v\n%s", err, output)
	}
}

func wantBatch3DNSAnswer(t *testing.T, server, name string, want net.IP) {
	t.Helper()
	waitFor(t, "DNS answer for "+name, func() bool {
		id := uint16(time.Now().UnixNano())
		query := make([]byte, 12)
		binary.BigEndian.PutUint16(query, id)
		binary.BigEndian.PutUint16(query[2:], 0x0100)
		binary.BigEndian.PutUint16(query[4:], 1)
		for _, label := range strings.Split(name, ".") {
			query = append(query, byte(len(label)))
			query = append(query, label...)
		}
		query = append(query, 0, 0, 1, 0, 1)
		conn, err := net.DialTimeout("udp4", net.JoinHostPort(server, "53"), time.Second)
		if err != nil {
			return false
		}
		defer conn.Close() //nolint:errcheck
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err = conn.Write(query); err != nil {
			return false
		}
		response := make([]byte, 1500)
		n, err := conn.Read(response)
		return err == nil && n >= 12 && binary.BigEndian.Uint16(response) == id &&
			binary.BigEndian.Uint16(response[6:]) > 0 && bytes.Contains(response[:n], want.To4())
	})
}
