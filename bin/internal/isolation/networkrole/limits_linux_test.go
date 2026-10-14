//go:build linux && amd64

package networkrole

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
)

func TestLinuxProductionLimitOptions(t *testing.T) {
	env := []string{"ALMAGEST_ISOLATION_MEMORY_BYTES", "ALMAGEST_ISOLATION_TASKS", "ALMAGEST_ISOLATION_CPU_QUOTA", "ALMAGEST_ISOLATION_DESCRIPTORS", "ALMAGEST_ISOLATION_FILE_BYTES"}
	for _, name := range env {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	parse := func(argv ...string) linuxprofile.Limits {
		t.Helper()
		var args daemonargs.Args
		parser, err := arg.NewParser(arg.Config{}, &args)
		if err != nil {
			t.Fatal(err)
		}
		if err := parser.Parse(argv); err != nil {
			t.Fatal(err)
		}
		limits, err := linuxProductionLimits(args, linuxprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return limits
	}
	want := linuxprofile.Limits{MemoryBytes: 8 << 30, Tasks: 4096, CPUQuota: 6400000, Descriptors: 65536, FileBytes: 1 << 40}
	if got := parse(); got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	values := []string{"536870912", "128", "100000", "1024", "268435456"}
	for i, name := range env {
		t.Setenv(name, values[i])
	}
	want = linuxprofile.Limits{MemoryBytes: 512 << 20, Tasks: 128, CPUQuota: 100000, Descriptors: 1024, FileBytes: 256 << 20}
	if got := parse(); got != want {
		t.Fatalf("environment = %+v, want %+v", got, want)
	}
	want = linuxprofile.Limits{MemoryBytes: 32 << 20, Tasks: 8, CPUQuota: 1000, Descriptors: 32, FileBytes: 1 << 20}
	if got := parse("--isolation-memory-bytes=33554432", "--isolation-tasks=8", "--isolation-cpu-quota=1000", "--isolation-descriptors=32", "--isolation-file-bytes=1048576"); got != want {
		t.Fatalf("CLI override = %+v, want %+v", got, want)
	}
}

func TestLinuxProductionInvalidLimits(t *testing.T) {
	for _, option := range []string{"memory-bytes", "tasks", "cpu-quota", "descriptors", "file-bytes"} {
		for _, value := range []string{"0", "18446744073709551615"} {
			t.Run(option+"/"+value, func(t *testing.T) {
				var args daemonargs.Args
				parser, err := arg.NewParser(arg.Config{}, &args)
				if err != nil {
					t.Fatal(err)
				}
				flag := "--isolation-" + option
				if err := parser.Parse([]string{flag + "=" + value}); err != nil {
					t.Fatal(err)
				}
				// Invalid limits must fail before reading config or starting roles.
				err = RunLinuxProduction(context.Background(), LinuxProduction{Arguments: args}, nil)
				if !errors.Is(err, linuxprofile.ErrUnavailable) || !strings.Contains(err.Error(), flag) {
					t.Fatalf("invalid startup option: %v", err)
				}
			})
		}
	}
}

func TestLinuxProductionLimitBase(t *testing.T) {
	base := linuxprofile.Limits{MemoryBytes: 512 << 20, Tasks: 128, CPUQuota: 100000, Descriptors: 1024, FileBytes: 256 << 20}
	quota := uint64(200000)
	got, err := linuxProductionLimits(daemonargs.Args{IsolationCPUQuota: &quota}, base)
	base.CPUQuota = quota
	if err != nil || got != base {
		t.Fatalf("base with override = %+v, %v; want %+v", got, err, base)
	}
}
