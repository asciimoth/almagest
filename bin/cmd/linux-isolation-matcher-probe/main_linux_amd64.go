//go:build linux && amd64

// linux-isolation-matcher-probe measures matcher RPC cost from the restricted
// checkpoint 10 runtime. It is not linked to almagestd.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"time"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/isolation/matcherexperiment"
	"golang.org/x/sys/unix"
)

const (
	auditArchX86_64 = 0xc000003e
	seccompSetMode  = 1
	seccompTSync    = 1
	seccompRetKill  = 0x80000000
	seccompRetErrno = 0x00050000
	seccompRetAllow = 0x7fff0000
)

type measurement struct {
	Candidate       string  `json:"candidate"`
	Flow            string  `json:"flow"`
	Run             int     `json:"run"`
	Temperature     string  `json:"temperature"`
	Samples         int     `json:"samples"`
	DurationNS      int64   `json:"duration_ns"`
	LookupsSecond   float64 `json:"lookups_second"`
	P50NS           int64   `json:"p50_ns"`
	P95NS           int64   `json:"p95_ns"`
	P99NS           int64   `json:"p99_ns"`
	IPCMessageCount int     `json:"ipc_message_count"`
}

type report struct {
	SchemaVersion      int           `json:"schema_version"`
	Profile            string        `json:"profile"`
	GoVersion          string        `json:"go_version"`
	LogMode            string        `json:"log_mode"`
	Runs               int           `json:"runs"`
	SamplesPerRun      int           `json:"samples_per_run"`
	Measurements       []measurement `json:"measurements"`
	MalformedRejected  bool          `json:"malformed_rejected"`
	FreshTupleAccepted bool          `json:"fresh_tuple_accepted"`
	NoObservationLease bool          `json:"no_observation_lease"`
	NativeSocketDenied bool          `json:"native_socket_denied"`
}

func count(name string, fallback int) int {
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
				fmt.Fprintln(os.Stderr, "matcher diagnostic load")
			}
		}
	}()
	return func() { close(stop); <-done }
}

func sendRequest(request matcherexperiment.Request) (matcherexperiment.Response, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return matcherexperiment.Response{}, err
	}
	if len(data) > matcherexperiment.MaxMessageBytes {
		return matcherexperiment.Response{}, errors.New("matcher request is too large")
	}
	if err = unix.Send(3, data, 0); err != nil {
		return matcherexperiment.Response{}, err
	}
	buffer := make([]byte, matcherexperiment.MaxMessageBytes)
	read, _, err := unix.Recvfrom(3, buffer, 0)
	if err != nil {
		return matcherexperiment.Response{}, err
	}
	var response matcherexperiment.Response
	return response, json.Unmarshal(buffer[:read], &response)
}

func receiveHello() (matcherexperiment.Response, error) {
	buffer := make([]byte, matcherexperiment.MaxMessageBytes)
	read, _, err := unix.Recvfrom(3, buffer, 0)
	if err != nil {
		return matcherexperiment.Response{}, err
	}
	var response matcherexperiment.Response
	return response, json.Unmarshal(buffer[:read], &response)
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

func run() (report, error) {
	fd, socketErr := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if socketErr == nil {
		_ = unix.Close(fd)
	}
	if !errors.Is(socketErr, unix.EPERM) || os.Getpid() != 1 || os.Getuid() != 65534 {
		return report{}, errors.New("matcher runtime is not restricted")
	}
	if err := finalFilter(); err != nil {
		return report{}, err
	}
	hello, err := receiveHello()
	if err != nil || len(hello.Flows) != 4 {
		return report{}, fmt.Errorf("matcher hello: %w", err)
	}
	runs, samples := count("ALMAGEST_DATA_PATH_RUNS", 7), count("ALMAGEST_MATCHER_SAMPLES", 200)
	result := report{SchemaVersion: 1, Profile: "linux-runtime-r-v1", GoVersion: runtime.Version(), LogMode: os.Getenv("ALMAGEST_LOG_LOAD"), Runs: runs,
		SamplesPerRun: samples, NativeSocketDenied: true, NoObservationLease: true}
	if result.LogMode == "" {
		result.LogMode = "normal"
	}
	stopLogging := startLogLoad()
	defer stopLogging()
	for runIndex := range runs {
		for _, flow := range hello.Flows {
			direct, directErr := sendRequest(matcherexperiment.Request{Kind: "direct", Flow: flow.Tuple, Iterations: samples})
			if directErr != nil || direct.Error != "" || !direct.Matched {
				return report{}, fmt.Errorf("direct %s: %s: %w", flow.Name, direct.Error, directErr)
			}
			average := direct.DurationNS / int64(samples)
			result.Measurements = append(result.Measurements, measurement{Candidate: "in-process-native", Flow: flow.Name,
				Run: runIndex, Temperature: map[bool]string{true: "cold", false: "warm"}[runIndex == 0], Samples: samples,
				DurationNS: direct.DurationNS, LookupsSecond: float64(samples) / (float64(direct.DurationNS) / float64(time.Second)),
				P50NS: average, P95NS: average, P99NS: average})
			latencies := make([]int64, 0, samples)
			started := time.Now()
			for range samples {
				before := time.Now()
				response, requestErr := sendRequest(matcherexperiment.Request{Kind: "match", Flow: flow.Tuple})
				if requestErr != nil || response.Error != "" || !response.Matched {
					return report{}, fmt.Errorf("RPC %s: %s: %w", flow.Name, response.Error, requestErr)
				}
				latencies = append(latencies, time.Since(before).Nanoseconds())
			}
			duration := time.Since(started)
			slices.Sort(latencies)
			result.Measurements = append(result.Measurements, measurement{Candidate: "bounded-matcher-rpc", Flow: flow.Name,
				Run: runIndex, Temperature: map[bool]string{true: "cold", false: "warm"}[runIndex == 0], Samples: samples,
				DurationNS: duration.Nanoseconds(), LookupsSecond: float64(samples) / duration.Seconds(),
				P50NS: percentile(latencies, 50), P95NS: percentile(latencies, 95), P99NS: percentile(latencies, 99), IPCMessageCount: samples * 2})
		}
	}
	if err = unix.Send(3, []byte("{"), 0); err != nil {
		return report{}, err
	}
	buffer := make([]byte, matcherexperiment.MaxMessageBytes)
	read, _, err := unix.Recvfrom(3, buffer, 0)
	if err != nil {
		return report{}, err
	}
	var malformed matcherexperiment.Response
	if err = json.Unmarshal(buffer[:read], &malformed); err != nil {
		return report{}, err
	}
	result.MalformedRejected = malformed.Error == "malformed_request"
	fresh := hello.Flows[0].Tuple
	fresh.LocalPort++
	freshResult, err := sendRequest(matcherexperiment.Request{Kind: "match", Flow: fresh})
	result.FreshTupleAccepted = err == nil && freshResult.Error != "limit_exceeded" && freshResult.Error != "observation_required"
	closeRequest, _ := json.Marshal(matcherexperiment.Request{Kind: "close"})
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
