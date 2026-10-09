package windowsprofile

import (
	"errors"
	"strings"
	"testing"
)

func TestPlans(t *testing.T) {
	for _, role := range []Role{Runtime, Network, Writer, Command} {
		if err := DefaultPlan(role).Validate(); err != nil {
			t.Fatal(err)
		}
		for _, memory := range []uint64{64 << 20, 1 << 40} {
			for _, cpu := range []uint32{1, 100} {
				if err := (Plan{role, memory, cpu}).Validate(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, p := range []Plan{{}, {Role: "other", MemoryBytes: 512 << 20, CPUPercent: 1}, {Runtime, 0, 1}, {Runtime, (64 << 20) - 1, 1}, {Runtime, (1 << 40) + 1, 1}, {Runtime, 64 << 20, 0}, {Runtime, 64 << 20, 101}} {
		if !errors.Is(p.Validate(), ErrUnavailable) {
			t.Fatalf("accepted %+v", p)
		}
	}
	for mask := 0; mask < 8; mask++ {
		if (CheckFeatures(mask&1 != 0, mask&2 != 0, mask&4 != 0) == nil) != (mask == 0) {
			t.Fatal(mask)
		}
	}
	for _, platform := range []struct {
		build uint32
		arch  string
	}{{20348, "amd64"}, {20348, "arm64"}, {20349, "amd64"}, {0, "amd64"}, {20348, ""}} {
		if (CheckPlatform(platform.build, platform.arch) == nil) != (platform.build == 20348 && platform.arch == "amd64") {
			t.Fatal(platform)
		}
	}
}

func TestPathValidation(t *testing.T) {
	for _, path := range []string{`C:\trusted\role.exe`, `d:\one space\storage`, `C:\foo.bar`, `C:\café`} {
		if err := validatePath(path); err != nil {
			t.Fatalf("%q: %v", path, err)
		}
	}
	for _, path := range []string{"C:\\a\xff", "C:\\a\nb", `C:\CONIN$`, `C:\CONOUT$`, `C:\COM¹.txt`, `C:\LPT²`} {
		if !errors.Is(validatePath(path), ErrUnavailable) {
			t.Fatalf("accepted alias %q", path)
		}
	}
	for _, path := range []string{"", `C:\`, `C:relative`, `\\server\share`, `\\?\C:\file`, `\??\C:\file`, `C:/file`, `C:\file:stream`, `C:\a\..\b`, `C:\a\.\b`, `C:\a\\b`, `C:\a\`, `C:\CON.txt`, `C:\NUL`, `C:\COM1`, `C:\LPT9.foo`, `C:\a.`, `C:\a `, `C:\a*`, `C:\a?`, `C:\SHORT~1`, "C:\\a\x00b", `C:\` + strings.Repeat("a", 241)} {
		if !errors.Is(validatePath(path), ErrUnavailable) {
			t.Fatalf("accepted %q", path)
		}
	}
}

func FuzzPlan(f *testing.F) {
	f.Add("runtime", uint64(512<<20), uint32(25), uint32(20348), "amd64")
	f.Add("network", uint64(0), uint32(101), uint32(0), "arm64")
	f.Fuzz(func(t *testing.T, role string, memory uint64, cpu, build uint32, arch string) {
		p := Plan{Role(role), memory, cpu}
		if err := p.Validate(); err == nil {
			if memory < 64<<20 || memory > 1<<40 || cpu < 1 || cpu > 100 {
				t.Fatal("unbounded job")
			}
			if p.Role != Runtime && p.Role != Network && p.Role != Writer && p.Role != Command {
				t.Fatal("unknown role")
			}
		} else if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		if CheckPlatform(build, arch) == nil && (build != 20348 || arch != "amd64") {
			t.Fatal("unqualified platform")
		}
	})
}

func FuzzPath(f *testing.F) {
	for _, path := range []string{`C:\safe\file`, `C:\..\a`, `C:\file:stream`, `\\?\C:\file`, ""} {
		f.Add(path)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if err := validatePath(path); err == nil {
			if len(path) > 240 || len(path) < 4 || path[1:3] != `:\` || strings.ContainsAny(path[3:], ":/\x00~") {
				t.Fatal("unsafe path accepted")
			}
		} else if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	})
}
