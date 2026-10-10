//go:build linux

package linuxprofile

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"testing"
)

func backendCgroupFixture() map[string]string {
	return map[string]string{"memory.max": "536870912\n", "memory.swap.max": "0\n", "pids.max": "128\n", "cpu.max": "100000 100000\n", "cgroup.procs": "11\n42\n"}
}

func TestBackendCgroupLimits(t *testing.T) {
	base := backendCgroupFixture()
	if err := validateBackendCgroup(base, DefaultLimits(), 42); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][]string{
		"memory.max":      {"", "max\n", "0\n", "536870913\n", "+1\n", "1\n\n", "18446744073709551616\n"},
		"memory.swap.max": {"", "max\n", "1\n"},
		"pids.max":        {"", "max\n", "0\n", "129\n", "-1\n"},
		"cpu.max":         {"", "max 100000\n", "100001 100000\n", "1 200000\n", "0 100000\n", "1  100000\n", "1 100000 extra\n"},
		"cgroup.procs":    {"", "11\n", "0\n42\n", "42\ninvalid\n", "+42\n"},
	} {
		for i, value := range values {
			t.Run(fmt.Sprintf("%s/%d", name, i), func(t *testing.T) {
				input := maps.Clone(base)
				input[name] = value
				if err := validateBackendCgroup(input, DefaultLimits(), 42); err == nil {
					t.Fatal("unsafe cgroup accepted")
				}
			})
		}
	}
}

func TestBackendRejectsUnprovisionedInputs(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close() //nolint:errcheck
	if err := directory.Chmod(0700); err != nil {
		t.Fatal(err)
	}
	for _, inputs := range [][2]*os.File{{nil, nil}, {directory, nil}, {directory, directory}} {
		if err := PrepareBackend(inputs[0], inputs[1], DefaultLimits()); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unprovisioned backend accepted", err)
		}
	}
	if err := PrepareBackend(directory, directory, Limits{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("invalid limits accepted", err)
	}
	if err := directory.Chmod(0755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareBackend(directory, directory, DefaultLimits()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("public state directory accepted", err)
	}
}

func FuzzBackendCgroup(f *testing.F) {
	f.Add("536870912\n", "0\n", "128\n", "100000 100000\n", "42\n")
	f.Add("max", "max", "max", "max 100000", "42\n")
	f.Add("08\n", "0\n", "09\n", "100000 100000\n", "42\n")
	f.Fuzz(func(t *testing.T, memory, swap, tasks, cpu, members string) {
		values := map[string]string{"memory.max": memory, "memory.swap.max": swap, "pids.max": tasks, "cpu.max": cpu, "cgroup.procs": members}
		if err := validateBackendCgroup(values, DefaultLimits(), 42); err != nil {
			return
		}
		for name, maximum := range map[string]uint64{"memory.max": DefaultLimits().MemoryBytes, "pids.max": DefaultLimits().Tasks} {
			var n uint64
			if _, err := fmt.Sscanf(values[name], "%d", &n); err != nil || n == 0 || n > maximum {
				t.Fatal("unbounded cgroup accepted")
			}
		}
		values["cgroup.procs"] = strconv.Itoa(41) + "\n"
		if validateBackendCgroup(values, DefaultLimits(), 42) == nil {
			t.Fatal("foreign cgroup accepted")
		}
	})
}
