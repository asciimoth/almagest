//go:build linux && amd64

// linux-isolation-tun-probe measures native TUN relay cost from the restricted
// checkpoint 10 runtime. It is not linked to almagestd.
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
	auditArch          = 0xc000003e
	seccompModeFilter  = 1
	seccompFilterTSync = 1
)

type measurement struct {
	Candidate       string  `json:"candidate"`
	Family          string  `json:"family"`
	Workload        string  `json:"workload"`
	PacketBytes     int     `json:"packet_bytes"`
	BatchSize       int     `json:"batch_size"`
	Run             int     `json:"run"`
	Temperature     string  `json:"temperature"`
	Samples         int     `json:"samples"`
	DurationNS      int64   `json:"duration_ns"`
	PacketsSecond   float64 `json:"packets_second"`
	P50NS           int64   `json:"p50_ns"`
	P95NS           int64   `json:"p95_ns"`
	P99NS           int64   `json:"p99_ns"`
	IPCMessageCount int     `json:"ipc_message_count"`
	CopiedBytes     int64   `json:"copied_bytes"`
}

type report struct {
	SchemaVersion      int           `json:"schema_version"`
	Profile            string        `json:"profile"`
	GoVersion          string        `json:"go_version"`
	LogMode            string        `json:"log_mode"`
	Runs               int           `json:"runs"`
	SamplesPerRun      int           `json:"samples_per_run"`
	NativeSocketDenied bool          `json:"native_socket_denied"`
	PayloadIntegrity   bool          `json:"payload_integrity"`
	Measurements       []measurement `json:"measurements"`
	Limitations        []string      `json:"limitations"`
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
	if mode == "disabled" {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
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
				fmt.Fprintln(os.Stderr, "TUN diagnostic load")
			}
		}
	}()
	return func() { close(stop); <-done }
}

func frame(operation byte, batch, packetBytes, iterations int, wireguard bool) []byte {
	value := make([]byte, 10+batch*packetBytes)
	value[0], value[1] = operation, byte(batch)
	binary.BigEndian.PutUint32(value[2:6], uint32(batch*packetBytes))
	binary.BigEndian.PutUint32(value[6:10], uint32(iterations))
	for index := 10; index < len(value); index++ {
		value[index] = byte(index*17 + packetBytes)
	}
	if wireguard {
		for offset := 10; offset < len(value); offset += packetBytes {
			value[offset] = 4
		}
	}
	return value
}

func exchange(request []byte, responseBytes int) ([]byte, error) {
	if err := unix.Send(3, request, 0); err != nil {
		return nil, err
	}
	response := make([]byte, responseBytes)
	read, _, err := unix.Recvfrom(3, response, 0)
	if err != nil {
		return nil, err
	}
	if read != responseBytes {
		return nil, fmt.Errorf("TUN response length %d", read)
	}
	return response, nil
}

func receiveTunDescriptors(want int) ([]int, error) {
	header, control := make([]byte, 10), make([]byte, unix.CmsgSpace(want*4))
	read, controlRead, _, _, err := unix.Recvmsg(3, header, control, 0)
	if err != nil {
		return nil, err
	}
	if read != len(header) {
		return nil, fmt.Errorf("TUN descriptor response length %d", read)
	}
	messages, err := unix.ParseSocketControlMessage(control[:controlRead])
	if err != nil || len(messages) != 1 {
		return nil, fmt.Errorf("parse TUN descriptor: %w", err)
	}
	descriptors, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(descriptors) != want {
		return nil, fmt.Errorf("TUN descriptor count %d: %w", len(descriptors), err)
	}
	return descriptors, nil
}

func packetChecksum(data []byte, initial uint32) uint16 {
	sum := initial
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func reverseTunPacket(packet []byte) error {
	if len(packet) < 28 {
		return errors.New("short direct TUN packet")
	}
	if packet[0]>>4 == 4 {
		header := int(packet[0]&0x0f) * 4
		if header < 20 || len(packet) < header+8 || packet[9] != unix.IPPROTO_UDP {
			return errors.New("invalid direct IPv4 packet")
		}
		for index := range 4 {
			packet[12+index], packet[16+index] = packet[16+index], packet[12+index]
		}
		packet[10], packet[11] = 0, 0
		binary.BigEndian.PutUint16(packet[10:12], packetChecksum(packet[:header], 0))
		packet[header], packet[header+2] = packet[header+2], packet[header]
		packet[header+1], packet[header+3] = packet[header+3], packet[header+1]
		packet[header+6], packet[header+7] = 0, 0
		return nil
	}
	if packet[0]>>4 != 6 || len(packet) < 48 || packet[6] != unix.IPPROTO_UDP {
		return errors.New("invalid direct IPv6 packet")
	}
	for index := range 16 {
		packet[8+index], packet[24+index] = packet[24+index], packet[8+index]
	}
	packet[40], packet[42] = packet[42], packet[40]
	packet[41], packet[43] = packet[43], packet[41]
	packet[46], packet[47] = 0, 0
	udpLength := uint32(len(packet) - 40)
	pseudo := uint32(udpLength>>16) + uint32(udpLength&0xffff) + uint32(unix.IPPROTO_UDP)
	for offset := 8; offset < 40; offset += 2 {
		pseudo += uint32(binary.BigEndian.Uint16(packet[offset : offset+2]))
	}
	value := packetChecksum(packet[40:], pseudo)
	if value == 0 {
		value = 0xffff
	}
	binary.BigEndian.PutUint16(packet[46:48], value)
	return nil
}

func directTunMeasurement(operation byte, family, workload string, packetBytes, run, samples int, wireguard bool) (measurement, error) {
	request := frame(0x30|operation, 1, packetBytes, samples, wireguard)
	if err := unix.Send(3, request, 0); err != nil {
		return measurement{}, err
	}
	descriptors, err := receiveTunDescriptors(1)
	if err != nil {
		return measurement{}, err
	}
	descriptor := descriptors[0]
	defer func() { _ = unix.Close(descriptor) }()
	latencies := make([]int64, 0, samples)
	packet := make([]byte, 65536)
	started := time.Now()
	for range samples {
		before := time.Now()
		var read, packetOffset int
		for skipped := 0; ; skipped++ {
			for {
				read, err = unix.Read(descriptor, packet)
				if !errors.Is(err, unix.EAGAIN) {
					break
				}
				_, _ = unix.Poll([]unix.PollFd{{Fd: int32(descriptor), Events: unix.POLLIN}}, 2000)
			}
			if err != nil {
				return measurement{}, err
			}
			packetOffset = 0
			if read >= 11 && packet[0]>>4 != 4 && packet[0]>>4 != 6 {
				packetOffset = 10 // Linux virtio-net header on a directly transferred TUN file.
			}
			payload := packet[packetOffset:read]
			if (operation == 1 && len(payload) >= 20 && payload[0]>>4 == 4 && payload[9] == unix.IPPROTO_UDP) ||
				(operation == 2 && len(payload) >= 40 && payload[0]>>4 == 6 && payload[6] == unix.IPPROTO_UDP) {
				break
			}
			if skipped >= 31 {
				return measurement{}, errors.New("direct TUN did not produce a UDP packet")
			}
		}
		if err = reverseTunPacket(packet[packetOffset:read]); err != nil {
			return measurement{}, err
		}
		clear(packet[:packetOffset])
		if _, err = unix.Write(descriptor, packet[:read]); err != nil {
			return measurement{}, err
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
	}
	duration := time.Since(started)
	slices.Sort(latencies)
	return measurement{Candidate: "broker-configured-direct-handle", Family: family, Workload: workload, PacketBytes: packetBytes,
		BatchSize: 1, Run: run, Temperature: map[bool]string{true: "cold", false: "warm"}[run == 0], Samples: samples,
		DurationNS: duration.Nanoseconds(), PacketsSecond: float64(samples) / duration.Seconds(), P50NS: percentile(latencies, 50),
		P95NS: percentile(latencies, 95), P99NS: percentile(latencies, 99)}, nil
}

func sharedTunMeasurement(operation byte, family, workload string, packetBytes, batch, run, samples int, wireguard bool) (measurement, error) {
	request := frame(0x20|operation, batch, packetBytes, samples, wireguard)
	if err := unix.Send(3, request, 0); err != nil {
		return measurement{}, err
	}
	descriptors, err := receiveTunDescriptors(3)
	if err != nil {
		return measurement{}, err
	}
	defer func() {
		for _, descriptor := range descriptors {
			_ = unix.Close(descriptor)
		}
	}()
	mapping, err := unix.Mmap(descriptors[0], 0, 65536, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return measurement{}, err
	}
	defer func() { _ = unix.Munmap(mapping) }()
	payload := request[10:]
	binary.LittleEndian.PutUint32(mapping[:4], uint32(len(payload)))
	copy(mapping[4:], payload)
	signal := make([]byte, 8)
	binary.LittleEndian.PutUint64(signal, 1)
	latencies := make([]int64, 0, samples)
	started := time.Now()
	for range samples {
		before := time.Now()
		if _, err = unix.Write(descriptors[1], signal); err != nil {
			return measurement{}, err
		}
		if _, err = unix.Read(descriptors[2], signal); err != nil {
			return measurement{}, err
		}
		if !slices.Equal(mapping[4:4+len(payload)], payload) {
			return measurement{}, errors.New("shared TUN payload changed")
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
	}
	duration := time.Since(started)
	slices.Sort(latencies)
	packets := samples * batch
	return measurement{Candidate: "shared-memory-relay", Family: family, Workload: workload, PacketBytes: packetBytes, BatchSize: batch,
		Run: run, Temperature: map[bool]string{true: "cold", false: "warm"}[run == 0], Samples: packets, DurationNS: duration.Nanoseconds(),
		PacketsSecond: float64(packets) / duration.Seconds(), P50NS: percentile(latencies, 50), P95NS: percentile(latencies, 95),
		P99NS: percentile(latencies, 99), IPCMessageCount: samples * 2, CopiedBytes: int64(packetBytes * packets * 2)}, nil
}

func percentile(sorted []int64, percent int) int64 {
	index := (len(sorted)*percent + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func finalFilter() error {
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, {Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: auditArch},
		{Code: unix.BPF_RET | unix.BPF_K, K: 0x80000000}, {Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.SYS_EXECVE}, {Code: unix.BPF_RET | unix.BPF_K, K: 0x00050000 | uint32(unix.EPERM)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.SYS_EXECVEAT}, {Code: unix.BPF_RET | unix.BPF_K, K: 0x00050000 | uint32(unix.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: 0x7fff0000},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, seccompModeFilter, seccompFilterTSync, uintptr(unsafe.Pointer(&program)))
	if errno != 0 {
		return errno
	}
	return nil
}

func run() (report, error) {
	fd, socketErr := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if socketErr == nil {
		_ = unix.Close(fd)
	}
	if !errors.Is(socketErr, unix.EPERM) || os.Getpid() != 1 || os.Getuid() != 65534 {
		return report{}, errors.New("TUN runtime is not restricted")
	}
	if err := finalFilter(); err != nil {
		return report{}, err
	}
	runs, samples := envCount("ALMAGEST_DATA_PATH_RUNS", 7), envCount("ALMAGEST_TUN_SAMPLES", 100)
	result := report{SchemaVersion: 1, Profile: "linux-runtime-r-v1", GoVersion: runtime.Version(), LogMode: os.Getenv("ALMAGEST_LOG_LOAD"), Runs: runs,
		SamplesPerRun: samples, NativeSocketDenied: true, PayloadIntegrity: true,
		Limitations: []string{"This stage measures one native TUN queue; the host capability decides the effective maximum queue count."}}
	if result.LogMode == "" {
		result.LogMode = "normal"
	}
	stopLogging := startLogLoad()
	defer stopLogging()
	for runIndex := range runs {
		for familyIndex, family := range []string{"ipv4", "ipv6"} {
			for _, workload := range []string{"plain", "wireguard-shaped"} {
				wireguard := workload != "plain"
				for _, size := range []int{64, 256, 512, 1200, 1500} {
					directRequest := frame(0x10|byte(familyIndex+1), 1, size, samples, wireguard)
					directResponse, err := exchange(directRequest, 18)
					if err != nil {
						return report{}, err
					}
					duration := int64(binary.BigEndian.Uint64(directResponse[10:]))
					average := duration / int64(samples)
					result.Measurements = append(result.Measurements, measurement{Candidate: "in-process-native", Family: family, Workload: workload,
						PacketBytes: size, BatchSize: 1, Run: runIndex, Temperature: map[bool]string{true: "cold", false: "warm"}[runIndex == 0],
						Samples: samples, DurationNS: duration, PacketsSecond: float64(samples) / (float64(duration) / float64(time.Second)),
						P50NS: average, P95NS: average, P99NS: average})
					directHandle, directErr := directTunMeasurement(byte(familyIndex+1), family, workload, size, runIndex, samples, wireguard)
					if directErr != nil {
						return report{}, directErr
					}
					result.Measurements = append(result.Measurements, directHandle)
					for _, batch := range []int{1, 16} {
						request := frame(byte(familyIndex+1), batch, size, 1, wireguard)
						latencies := make([]int64, 0, samples)
						started := time.Now()
						for range samples {
							before := time.Now()
							response, exchangeErr := exchange(request, len(request))
							if exchangeErr != nil {
								return report{}, exchangeErr
							}
							result.PayloadIntegrity = result.PayloadIntegrity && slices.Equal(response, request)
							latencies = append(latencies, time.Since(before).Nanoseconds())
						}
						total := time.Since(started)
						slices.Sort(latencies)
						packets := samples * batch
						result.Measurements = append(result.Measurements, measurement{Candidate: "broker-relay", Family: family, Workload: workload,
							PacketBytes: size, BatchSize: batch, Run: runIndex, Temperature: map[bool]string{true: "cold", false: "warm"}[runIndex == 0],
							Samples: packets, DurationNS: total.Nanoseconds(), PacketsSecond: float64(packets) / total.Seconds(),
							P50NS: percentile(latencies, 50), P95NS: percentile(latencies, 95), P99NS: percentile(latencies, 99),
							IPCMessageCount: samples * 2, CopiedBytes: int64(size * packets * 2)})
						shared, sharedErr := sharedTunMeasurement(byte(familyIndex+1), family, workload, size, batch, runIndex, samples, wireguard)
						if sharedErr != nil {
							return report{}, sharedErr
						}
						result.Measurements = append(result.Measurements, shared)
					}
				}
			}
		}
	}
	closeRequest := make([]byte, 10)
	closeRequest[0] = 0xff
	_ = unix.Send(3, closeRequest, 0)
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
}
