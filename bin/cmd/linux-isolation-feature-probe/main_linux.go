//go:build linux

// linux-isolation-feature-probe records the native Linux backend behavior for
// checkpoint 10 capability profiles. It runs only in the disposable harness.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"github.com/asciimoth/gonnect/sysnet"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"golang.org/x/sys/unix"
)

type capabilityCounts map[string]int

type featureReport struct {
	Profile            string                             `json:"profile"`
	Kernel             string                             `json:"kernel"`
	GoVersion          string                             `json:"go_version"`
	UID                int                                `json:"uid"`
	EffectiveCaps      string                             `json:"effective_capabilities"`
	TunDevice          string                             `json:"tun_device"`
	TCPCreate          string                             `json:"tcp_create"`
	RawSocket          string                             `json:"raw_socket"`
	BPF                string                             `json:"bpf"`
	NetlinkRoute       string                             `json:"netlink_route"`
	DNSProviderSignals map[string]string                  `json:"dns_provider_signals"`
	BackendCounts      map[sysnet.Target]capabilityCounts `json:"backend_capability_counts"`
	BackendLogs        []string                           `json:"backend_logs"`
	ConstructionError  string                             `json:"construction_error,omitempty"`
}

func capabilityStateName(state sysnet.CapabilityState) string {
	switch state {
	case sysnet.CapabilityUnknown:
		return "unknown"
	case sysnet.CapabilityUnsupported:
		return "unsupported"
	case sysnet.CapabilityUnavailable:
		return "unavailable"
	case sysnet.CapabilityAvailable:
		return "available"
	default:
		return fmt.Sprintf("invalid-%d", state)
	}
}

func syscallResult(err error) string {
	if err == nil {
		return "available"
	}
	return err.Error()
}

func effectiveCapabilities() string {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "unavailable: " + err.Error()
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
		}
	}
	return "unavailable: CapEff missing"
}

func probeTun() string {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = unix.Close(fd) }()
	var request struct {
		Name  [unix.IFNAMSIZ]byte
		Flags uint16
		_     [22]byte
	}
	copy(request.Name[:], "alm-exp-probe")
	request.Flags = unix.IFF_TUN | unix.IFF_NO_PI
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETIFF,
		uintptr(unsafe.Pointer(&request)))
	if errno != 0 {
		return errno.Error()
	}
	return "available"
}

func probeBPF() string {
	var attributes [256]byte
	*(*uint32)(unsafe.Pointer(&attributes[0])) = uint32(unix.BPF_MAP_TYPE_ARRAY)
	*(*uint32)(unsafe.Pointer(&attributes[4])) = 4
	*(*uint32)(unsafe.Pointer(&attributes[8])) = 4
	*(*uint32)(unsafe.Pointer(&attributes[12])) = 1
	fd, _, errno := unix.Syscall(unix.SYS_BPF, unix.BPF_MAP_CREATE,
		uintptr(unsafe.Pointer(&attributes[0])), uintptr(len(attributes)))
	if errno != 0 {
		return errno.Error()
	}
	_ = unix.Close(int(fd))
	return "available"
}

func main() {
	var uts unix.Utsname
	kernel := "unknown"
	if err := unix.Uname(&uts); err == nil {
		kernel = unix.ByteSliceToString(uts.Release[:])
	}
	report := featureReport{
		Profile:            os.Getenv("ALMAGEST_FEATURE_PROFILE"),
		Kernel:             kernel,
		GoVersion:          runtime.Version(),
		UID:                os.Getuid(),
		EffectiveCaps:      effectiveCapabilities(),
		TunDevice:          probeTun(),
		BPF:                probeBPF(),
		DNSProviderSignals: map[string]string{},
		BackendCounts:      map[sysnet.Target]capabilityCounts{},
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	report.TCPCreate = syscallResult(err)
	if err == nil {
		_ = unix.Close(fd)
	}
	fd, err = unix.Socket(unix.AF_INET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.IPPROTO_ICMP)
	report.RawSocket = syscallResult(err)
	if err == nil {
		_ = unix.Close(fd)
	}
	fd, err = unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	report.NetlinkRoute = syscallResult(err)
	if err == nil {
		_ = unix.Close(fd)
	}
	for name, path := range map[string]string{
		"resolv_conf": "/etc/resolv.conf",
		"system_bus":  "/run/dbus/system_bus_socket",
		"resolvconf":  "/sbin/resolvconf",
	} {
		if info, statErr := os.Stat(path); statErr != nil {
			report.DNSProviderSignals[name] = statErr.Error()
		} else {
			report.DNSProviderSignals[name] = info.Mode().String()
		}
	}

	system, constructionErr := linuxsysnet.New(linuxsysnet.SystemConfig{
		Logf: func(format string, values ...any) {
			report.BackendLogs = append(report.BackendLogs, fmt.Sprintf(format, values...))
		},
	})
	if constructionErr != nil {
		report.ConstructionError = constructionErr.Error()
	} else {
		capabilities := system.Capabilities()
		for _, operation := range capabilities.Operations {
			counts := report.BackendCounts[operation.Key.Target]
			if counts == nil {
				counts = capabilityCounts{}
				report.BackendCounts[operation.Key.Target] = counts
			}
			counts[capabilityStateName(operation.State)]++
		}
		_ = system.Close()
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
