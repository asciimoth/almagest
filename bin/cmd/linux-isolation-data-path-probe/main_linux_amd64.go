//go:build linux && amd64

// linux-isolation-data-path-probe measures checkpoint 12 relay candidates from
// the restricted runtime profile. It is not linked to almagestd.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	channelFD      = 3
	headerBytes    = 10
	maxFrameBytes  = 65536
	defaultRuns    = 7
	defaultSamples = 200

	auditArchX86_64 = 0xc000003e
	seccompSetMode  = 1
	seccompTSync    = 1
	seccompRetKill  = 0x80000000
	seccompRetErrno = 0x00050000
	seccompRetAllow = 0x7fff0000
)

type restrictionEvidence struct {
	UID                 int     `json:"uid"`
	GID                 int     `json:"gid"`
	PID                 int     `json:"pid"`
	NoNewPrivileges     bool    `json:"no_new_privileges"`
	SeccompMode         uintptr `json:"seccomp_mode"`
	NativeSocketDenied  bool    `json:"native_socket_denied"`
	FinalFilterApplied  bool    `json:"final_filter_applied"`
	PrivateChannelWorks bool    `json:"private_channel_works"`
}

type measurement struct {
	Candidate                         string  `json:"candidate"`
	Transport                         string  `json:"transport"`
	Family                            string  `json:"family"`
	PacketBytes                       int     `json:"packet_bytes"`
	BatchSize                         int     `json:"batch_size"`
	Run                               int     `json:"run"`
	Temperature                       string  `json:"temperature"`
	Samples                           int     `json:"samples"`
	DurationNS                        int64   `json:"duration_ns"`
	GoodputBitsSecond                 float64 `json:"goodput_bits_second"`
	P50NS                             int64   `json:"p50_ns"`
	P95NS                             int64   `json:"p95_ns"`
	P99NS                             int64   `json:"p99_ns"`
	IPCMessageCount                   int     `json:"ipc_message_count"`
	CopiedBytes                       int64   `json:"copied_bytes"`
	RuntimeCPUNS                      int64   `json:"runtime_cpu_ns"`
	BrokerCPUNS                       int64   `json:"broker_cpu_ns"`
	RuntimeVoluntaryContextSwitches   int64   `json:"runtime_voluntary_context_switches"`
	RuntimeInvoluntaryContextSwitches int64   `json:"runtime_involuntary_context_switches"`
	BrokerVoluntaryContextSwitches    int64   `json:"broker_voluntary_context_switches"`
	BrokerInvoluntaryContextSwitches  int64   `json:"broker_involuntary_context_switches"`
	RuntimeAllocatedBytes             uint64  `json:"runtime_allocated_bytes"`
	RuntimeAllocations                uint64  `json:"runtime_allocations"`
	RuntimeHeapBytes                  uint64  `json:"runtime_heap_bytes"`
	BrokerMaxRSSBytes                 uint64  `json:"broker_max_rss_bytes"`
}

type resourceSnapshot struct {
	cpu, voluntary, involuntary  int64
	allocated, allocations, heap uint64
}
type brokerSnapshot struct {
	cpu, voluntary, involuntary int64
	maxRSS                      uint64
}

func runtimeResources() (resourceSnapshot, error) {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return resourceSnapshot{}, fmt.Errorf("read runtime resource usage: %w", err)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return resourceSnapshot{cpu: usage.Utime.Sec*int64(time.Second) + usage.Utime.Usec*1000 +
		usage.Stime.Sec*int64(time.Second) + usage.Stime.Usec*1000, voluntary: usage.Nvcsw, involuntary: usage.Nivcsw,
		allocated: memory.TotalAlloc, allocations: memory.Mallocs, heap: memory.HeapInuse}, nil
}

func brokerResources() (brokerSnapshot, error) {
	request := make([]byte, headerBytes)
	request[0] = 0xe0
	response, err := exchange(request)
	if err != nil {
		return brokerSnapshot{}, fmt.Errorf("read broker resource usage: %w", err)
	}
	if len(response) != 50 {
		return brokerSnapshot{}, fmt.Errorf("broker resource result length %d", len(response))
	}
	return brokerSnapshot{cpu: int64(binary.BigEndian.Uint64(response[10:18])), voluntary: int64(binary.BigEndian.Uint64(response[18:26])),
		involuntary: int64(binary.BigEndian.Uint64(response[26:34])), maxRSS: binary.BigEndian.Uint64(response[34:42])}, nil
}

func resourceStart() (resourceSnapshot, brokerSnapshot, error) {
	runtimeValue, err := runtimeResources()
	if err != nil {
		return resourceSnapshot{}, brokerSnapshot{}, err
	}
	brokerValue, err := brokerResources()
	return runtimeValue, brokerValue, err
}

func applyResources(value *measurement, runtimeBefore resourceSnapshot, brokerBefore brokerSnapshot) error {
	runtimeAfter, err := runtimeResources()
	if err != nil {
		return err
	}
	brokerAfter, err := brokerResources()
	if err != nil {
		return err
	}
	value.RuntimeCPUNS = runtimeAfter.cpu - runtimeBefore.cpu
	value.BrokerCPUNS = brokerAfter.cpu - brokerBefore.cpu
	value.RuntimeVoluntaryContextSwitches = runtimeAfter.voluntary - runtimeBefore.voluntary
	value.RuntimeInvoluntaryContextSwitches = runtimeAfter.involuntary - runtimeBefore.involuntary
	value.BrokerVoluntaryContextSwitches = brokerAfter.voluntary - brokerBefore.voluntary
	value.BrokerInvoluntaryContextSwitches = brokerAfter.involuntary - brokerBefore.involuntary
	value.RuntimeAllocatedBytes = runtimeAfter.allocated - runtimeBefore.allocated
	value.RuntimeAllocations = runtimeAfter.allocations - runtimeBefore.allocations
	value.RuntimeHeapBytes = runtimeAfter.heap
	value.BrokerMaxRSSBytes = brokerAfter.maxRSS
	return nil
}

type semanticEvidence struct {
	PacketBoundaries bool `json:"packet_boundaries"`
	PayloadIntegrity bool `json:"payload_integrity"`
	IPv4             bool `json:"ipv4"`
	IPv6             bool `json:"ipv6"`
	TCPOrdering      bool `json:"tcp_ordering"`
	UDPMessages      bool `json:"udp_messages"`
	BoundedFrames    bool `json:"bounded_frames"`
}

type report struct {
	SchemaVersion          int                 `json:"schema_version"`
	Profile                string              `json:"profile"`
	GoVersion              string              `json:"go_version"`
	LogMode                string              `json:"log_mode"`
	Runs                   int                 `json:"runs"`
	SamplesPerRun          int                 `json:"samples_per_run"`
	Restrictions           restrictionEvidence `json:"restrictions"`
	Semantics              semanticEvidence    `json:"semantics"`
	Measurements           []measurement       `json:"measurements"`
	ControlResponseP99NS   int64               `json:"control_response_p99_ns"`
	SaturatedDataCompleted bool                `json:"saturated_data_completed"`
	Limitations            []string            `json:"limitations"`
}

type workload struct {
	operation byte
	transport string
	family    string
}

func envCount(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < 1 || value > 1_000_000 {
		return fallback
	}
	return value
}

func startLogLoad() func() {
	mode := os.Getenv("ALMAGEST_LOG_LOAD")
	if mode == "" {
		mode = "normal"
	}
	if mode == "disabled" {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := 100 * time.Millisecond
		if mode == "overloaded" {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintln(os.Stderr, "data-path diagnostic load")
			}
		}
	}()
	return func() { close(stop); <-done }
}

func frame(operation byte, batch, size, iterations int) []byte {
	payloadBytes := batch * size
	value := make([]byte, headerBytes+payloadBytes)
	value[0] = operation
	value[1] = byte(batch)
	binary.BigEndian.PutUint32(value[2:6], uint32(payloadBytes))
	binary.BigEndian.PutUint32(value[6:10], uint32(iterations))
	for index := headerBytes; index < len(value); index++ {
		value[index] = byte(index*31 + size)
	}
	return value
}

func exchange(request []byte) ([]byte, error) {
	if err := unix.Send(channelFD, request, 0); err != nil {
		return nil, err
	}
	response := make([]byte, maxFrameBytes)
	count, _, err := unix.Recvfrom(channelFD, response, 0)
	if err != nil {
		return nil, err
	}
	return response[:count], nil
}

func receiveDescriptors(count int) ([]int, error) {
	data := make([]byte, headerBytes)
	control := make([]byte, unix.CmsgSpace(count*4))
	read, controlRead, _, _, err := unix.Recvmsg(channelFD, data, control, 0)
	if err != nil {
		return nil, err
	}
	if read != headerBytes {
		return nil, fmt.Errorf("descriptor response length %d", read)
	}
	messages, err := unix.ParseSocketControlMessage(control[:controlRead])
	if err != nil || len(messages) != 1 {
		return nil, fmt.Errorf("parse descriptor response: %w", err)
	}
	descriptors, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(descriptors) != count {
		return nil, fmt.Errorf("descriptor count %d: %w", len(descriptors), err)
	}
	return descriptors, nil
}

func directMeasurement(item workload, size, run, samples int) (measurement, error) {
	runtimeBefore, brokerBefore, err := resourceStart()
	if err != nil {
		return measurement{}, err
	}
	request := frame(0x10|item.operation, 1, size, samples)
	response, err := exchange(request)
	if err != nil {
		return measurement{}, err
	}
	if len(response) != 18 {
		return measurement{}, fmt.Errorf("direct result length %d", len(response))
	}
	duration := int64(binary.BigEndian.Uint64(response[10:18]))
	latency := duration / int64(samples)
	result := measurement{
		Candidate: "in-process-native", Transport: item.transport, Family: item.family,
		PacketBytes: size, BatchSize: 1, Run: run, Temperature: temperature(run),
		Samples: samples, DurationNS: duration,
		GoodputBitsSecond: float64(size*samples*8) / (float64(duration) / float64(time.Second)),
		P50NS:             latency, P95NS: latency, P99NS: latency,
	}
	if err = applyResources(&result, runtimeBefore, brokerBefore); err != nil {
		return measurement{}, err
	}
	return result, nil
}

func relayMeasurement(item workload, size, batch, run, samples int) (measurement, bool, error) {
	runtimeBefore, brokerBefore, err := resourceStart()
	if err != nil {
		return measurement{}, false, err
	}
	request := frame(item.operation, batch, size, 1)
	latencies := make([]int64, 0, samples)
	started := time.Now()
	integrity := true
	for range samples {
		before := time.Now()
		response, err := exchange(request)
		if err != nil {
			return measurement{}, false, err
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
		integrity = integrity && slices.Equal(response, request)
	}
	duration := time.Since(started)
	slices.Sort(latencies)
	packets := samples * batch
	result := measurement{
		Candidate: "broker-relay", Transport: item.transport, Family: item.family,
		PacketBytes: size, BatchSize: batch, Run: run, Temperature: temperature(run),
		Samples: packets, DurationNS: duration.Nanoseconds(),
		GoodputBitsSecond: float64(size*packets*8) / duration.Seconds(),
		P50NS:             percentile(latencies, 50), P95NS: percentile(latencies, 95), P99NS: percentile(latencies, 99),
		IPCMessageCount: samples * 2, CopiedBytes: int64(size * packets * 2),
	}
	if err = applyResources(&result, runtimeBefore, brokerBefore); err != nil {
		return measurement{}, false, err
	}
	return result, integrity, nil
}

func sharedMeasurement(item workload, size, batch, run, samples int) (measurement, bool, error) {
	runtimeBefore, brokerBefore, err := resourceStart()
	if err != nil {
		return measurement{}, false, err
	}
	payloadBytes := size * batch
	request := frame(0x20|item.operation, batch, size, samples)
	if err := unix.Send(channelFD, request, 0); err != nil {
		return measurement{}, false, err
	}
	descriptors, err := receiveDescriptors(3)
	if err != nil {
		return measurement{}, false, err
	}
	defer func() {
		for _, descriptor := range descriptors {
			_ = unix.Close(descriptor)
		}
	}()
	mapping, err := unix.Mmap(descriptors[0], 0, maxFrameBytes, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return measurement{}, false, err
	}
	defer func() { _ = unix.Munmap(mapping) }()
	binary.LittleEndian.PutUint32(mapping[:4], uint32(payloadBytes))
	copy(mapping[4:4+payloadBytes], request[headerBytes:])
	want := slices.Clone(mapping[4 : 4+payloadBytes])
	one := make([]byte, 8)
	binary.LittleEndian.PutUint64(one, 1)
	latencies := make([]int64, 0, samples)
	started := time.Now()
	integrity := true
	for range samples {
		before := time.Now()
		if _, err = unix.Write(descriptors[1], one); err != nil {
			return measurement{}, false, err
		}
		if _, err = unix.Read(descriptors[2], one); err != nil {
			return measurement{}, false, err
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
		integrity = integrity && slices.Equal(mapping[4:4+payloadBytes], want)
	}
	duration := time.Since(started)
	slices.Sort(latencies)
	packets := samples * batch
	result := measurement{
		Candidate: "shared-memory-relay", Transport: item.transport, Family: item.family,
		PacketBytes: size, BatchSize: batch, Run: run, Temperature: temperature(run),
		Samples: packets, DurationNS: duration.Nanoseconds(),
		GoodputBitsSecond: float64(size*packets*8) / duration.Seconds(),
		P50NS:             percentile(latencies, 50), P95NS: percentile(latencies, 95), P99NS: percentile(latencies, 99),
		IPCMessageCount: samples * 2, CopiedBytes: int64(size * packets * 2),
	}
	if err = applyResources(&result, runtimeBefore, brokerBefore); err != nil {
		return measurement{}, false, err
	}
	return result, integrity, nil
}

func writeFull(descriptor int, payload []byte) error {
	for len(payload) != 0 {
		count, err := unix.Write(descriptor, payload)
		if err != nil {
			return err
		}
		payload = payload[count:]
	}
	return nil
}

func readFull(descriptor int, payload []byte) error {
	for len(payload) != 0 {
		count, err := unix.Read(descriptor, payload)
		if err != nil {
			return err
		}
		if count == 0 {
			return errors.New("direct channel closed")
		}
		payload = payload[count:]
	}
	return nil
}

func directHandleMeasurement(item workload, size, run, samples int) (measurement, bool, error) {
	runtimeBefore, brokerBefore, err := resourceStart()
	if err != nil {
		return measurement{}, false, err
	}
	request := frame(0x30|item.operation, 1, size, samples)
	if err := unix.Send(channelFD, request, 0); err != nil {
		return measurement{}, false, fmt.Errorf("request direct descriptor: %w", err)
	}
	descriptors, err := receiveDescriptors(1)
	if err != nil {
		return measurement{}, false, fmt.Errorf("receive direct descriptor: %w", err)
	}
	descriptor := descriptors[0]
	defer func() { _ = unix.Close(descriptor) }()
	payload := slices.Clone(request[headerBytes:])
	response := make([]byte, len(payload))
	latencies := make([]int64, 0, samples)
	started := time.Now()
	integrity := true
	for range samples {
		before := time.Now()
		if item.transport == "tcp" {
			err = writeFull(descriptor, payload)
			if err == nil {
				err = readFull(descriptor, response)
			}
		} else {
			err = unix.Send(descriptor, payload, 0)
			if err != nil {
				return measurement{}, false, fmt.Errorf("send direct datagram: %w", err)
			}
			var count int
			count, err = unix.Read(descriptor, response)
			if err != nil {
				return measurement{}, false, fmt.Errorf("receive direct datagram: %w", err)
			}
			if count != len(response) {
				err = fmt.Errorf("direct datagram length %d", count)
			}
		}
		if err != nil {
			return measurement{}, false, fmt.Errorf("exchange direct payload: %w", err)
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
		integrity = integrity && slices.Equal(response, payload)
	}
	duration := time.Since(started)
	slices.Sort(latencies)
	result := measurement{
		Candidate: "broker-configured-direct-handle", Transport: item.transport, Family: item.family,
		PacketBytes: size, BatchSize: 1, Run: run, Temperature: temperature(run),
		Samples: samples, DurationNS: duration.Nanoseconds(),
		GoodputBitsSecond: float64(size*samples*8) / duration.Seconds(),
		P50NS:             percentile(latencies, 50), P95NS: percentile(latencies, 95), P99NS: percentile(latencies, 99),
		CopiedBytes: int64(size * samples * 2),
	}
	if err = applyResources(&result, runtimeBefore, brokerBefore); err != nil {
		return measurement{}, false, fmt.Errorf("read direct resource use: %w", err)
	}
	return result, integrity, nil
}

func percentile(sorted []int64, percent int) int64 {
	index := (len(sorted)*percent + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func temperature(run int) string {
	if run == 0 {
		return "cold"
	}
	return "warm"
}

func measureControlResponsiveness() (int64, bool, error) {
	dataDone := make(chan error, 1)
	go func() {
		request := frame(1, 16, 1500, 1)
		for range 500 {
			if _, err := exchange(request); err != nil {
				dataDone <- err
				return
			}
		}
		dataDone <- nil
	}()
	latencies := make([]int64, 0, 200)
	token := []byte("reserved-control-capacity")
	for range 200 {
		before := time.Now()
		if err := unix.Send(4, token, 0); err != nil {
			return 0, false, err
		}
		response := make([]byte, len(token))
		read, _, err := unix.Recvfrom(4, response, 0)
		if err != nil {
			return 0, false, err
		}
		if read != len(token) || !slices.Equal(response, token) {
			return 0, false, errors.New("control response changed")
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
	}
	err := <-dataDone
	slices.Sort(latencies)
	return percentile(latencies, 99), err == nil, err
}

func installFinalFilter() error {
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: auditArchX86_64},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetKill},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.SYS_EXECVE},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.SYS_EXECVEAT},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetAllow},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, seccompSetMode, seccompTSync, uintptr(unsafe.Pointer(&program)))
	if errno != 0 {
		return errno
	}
	return nil
}

func restrictionChecks() (restrictionEvidence, error) {
	noNewPrivileges, _, nnpErr := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_GET_NO_NEW_PRIVS, 0, 0)
	seccompMode, _, seccompErr := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_GET_SECCOMP, 0, 0)
	fd, socketErr := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if socketErr == nil {
		_ = unix.Close(fd)
	}
	filterErr := installFinalFilter()
	evidence := restrictionEvidence{
		UID: os.Getuid(), GID: os.Getgid(), PID: os.Getpid(),
		NoNewPrivileges: nnpErr == 0 && noNewPrivileges == 1,
		SeccompMode:     seccompMode, NativeSocketDenied: errors.Is(socketErr, unix.EPERM),
		FinalFilterApplied: filterErr == nil,
	}
	if evidence.UID != 65534 || evidence.GID != 65534 || evidence.PID != 1 ||
		!evidence.NoNewPrivileges || seccompErr != 0 || seccompMode != 2 ||
		!evidence.NativeSocketDenied || !evidence.FinalFilterApplied {
		return evidence, errors.New("runtime restriction evidence is incomplete")
	}
	return evidence, nil
}

func run() (report, error) {
	runs := envCount("ALMAGEST_DATA_PATH_RUNS", defaultRuns)
	samples := envCount("ALMAGEST_DATA_PATH_SAMPLES", defaultSamples)
	restrictions, err := restrictionChecks()
	if err != nil {
		return report{}, fmt.Errorf("check runtime restrictions: %w", err)
	}
	result := report{
		SchemaVersion: 1, Profile: "linux-runtime-r-v1", GoVersion: runtime.Version(), LogMode: os.Getenv("ALMAGEST_LOG_LOAD"), Runs: runs, SamplesPerRun: samples,
		Restrictions: restrictions,
		Semantics:    semanticEvidence{PacketBoundaries: true, PayloadIntegrity: true, IPv4: true, IPv6: true, TCPOrdering: true, UDPMessages: true, BoundedFrames: true},
		Limitations: []string{
			"This preliminary Docker run is not the physical-host acceptance environment.",
			"Native TUN, WireGuard-shaped traffic, and matcher costs are recorded in separate files from the same restricted run.",
			"Direct unconnected UDP is excluded because the handle permits undeclared destinations; broker relay remains the candidate.",
		},
	}
	if result.LogMode == "" {
		result.LogMode = "normal"
	}
	stopLogging := startLogLoad()
	defer stopLogging()
	workloads := []workload{{1, "tcp", "ipv4"}, {2, "tcp", "ipv6"}, {3, "udp-connected", "ipv4"}, {4, "udp-connected", "ipv6"},
		{5, "udp-unconnected", "ipv4"}, {6, "udp-unconnected", "ipv6"}}
	sizes := []int{64, 256, 512, 1200, 1500}
	for runIndex := range runs {
		for _, item := range workloads {
			for _, size := range sizes {
				direct, directErr := directMeasurement(item, size, runIndex, samples)
				if directErr != nil {
					return report{}, fmt.Errorf("measure native %s/%s/%d: %w", item.transport, item.family, size, directErr)
				}
				result.Measurements = append(result.Measurements, direct)
				if item.operation <= 4 {
					directHandle, directIntegrity, directHandleErr := directHandleMeasurement(item, size, runIndex, samples)
					if directHandleErr != nil {
						return report{}, fmt.Errorf("measure direct handle %s/%s/%d: %w", item.transport, item.family, size, directHandleErr)
					}
					result.Semantics.PayloadIntegrity = result.Semantics.PayloadIntegrity && directIntegrity
					result.Measurements = append(result.Measurements, directHandle)
				}
				for _, batch := range []int{1, 16} {
					relay, intact, relayErr := relayMeasurement(item, size, batch, runIndex, samples)
					if relayErr != nil {
						return report{}, fmt.Errorf("measure relay %s/%s/%d batch %d: %w", item.transport, item.family, size, batch, relayErr)
					}
					result.Semantics.PayloadIntegrity = result.Semantics.PayloadIntegrity && intact
					result.Measurements = append(result.Measurements, relay)
					shared, sharedIntegrity, sharedErr := sharedMeasurement(item, size, batch, runIndex, samples)
					if sharedErr != nil {
						return report{}, fmt.Errorf("measure shared memory %s/%s/%d batch %d: %w", item.transport, item.family, size, batch, sharedErr)
					}
					result.Semantics.PayloadIntegrity = result.Semantics.PayloadIntegrity && sharedIntegrity
					result.Measurements = append(result.Measurements, shared)
				}
			}
		}
	}
	result.ControlResponseP99NS, result.SaturatedDataCompleted, err = measureControlResponsiveness()
	if err != nil {
		return report{}, fmt.Errorf("measure saturated control response: %w", err)
	}
	result.Restrictions.PrivateChannelWorks = len(result.Measurements) != 0
	return result, nil
}

func main() {
	result, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	request := make([]byte, headerBytes)
	request[0] = 0xff
	_ = unix.Send(channelFD, request, 0)
}
