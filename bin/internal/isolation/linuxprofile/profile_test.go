package linuxprofile

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func TestPlans(t *testing.T) {
	for _, role := range []Role{Runtime, Network, Writer, Command} {
		p := Plan{Role: role, UID: 65534, GID: 65534, Limits: DefaultLimits()}
		args, err := p.Arguments()
		if err != nil || len(args) != 10 || args[0] != "v1" || args[1] != string(role) {
			t.Fatalf("%s: %v %v", role, args, err)
		}
		for _, s := range args[2:] {
			if _, err := strconv.ParseUint(s, 10, 64); err != nil {
				t.Fatal(err)
			}
		}
		p.Capabilities = NetworkCapabilities
		if (p.Validate() == nil) != (role == Network) {
			t.Fatalf("capabilities accepted for %s", role)
		}
	}
}

func TestInvalidPlans(t *testing.T) {
	base := Plan{Role: Runtime, UID: 65534, GID: 65534, Limits: DefaultLimits()}
	cases := map[string]func(*Plan){
		"role":            func(p *Plan) { p.Role = "other" },
		"root-uid":        func(p *Plan) { p.UID = 0 },
		"root-gid":        func(p *Plan) { p.GID = 0 },
		"invalid-uid":     func(p *Plan) { p.UID = ^uint32(0) },
		"invalid-gid":     func(p *Plan) { p.GID = ^uint32(0) },
		"setup-caps":      func(p *Plan) { p.Role = Network; p.Capabilities = 1 << 21 },
		"bpf-caps":        func(p *Plan) { p.Role = Network; p.Capabilities = 1 << 39 },
		"descriptor-low":  func(p *Plan) { p.Limits.Descriptors = 31 },
		"descriptor-high": func(p *Plan) { p.Limits.Descriptors = 65537 },
		"file-low":        func(p *Plan) { p.Limits.FileBytes = (1 << 20) - 1 },
		"file-high":       func(p *Plan) { p.Limits.FileBytes = (1 << 40) + 1 },
		"memory-low":      func(p *Plan) { p.Limits.MemoryBytes = (32 << 20) - 1 },
		"memory-high":     func(p *Plan) { p.Limits.MemoryBytes = (1 << 40) + 1 },
		"tasks-low":       func(p *Plan) { p.Limits.Tasks = 7 },
		"tasks-high":      func(p *Plan) { p.Limits.Tasks = 4097 },
		"cpu-low":         func(p *Plan) { p.Limits.CPUQuota = 999 },
		"cpu-high":        func(p *Plan) { p.Limits.CPUQuota = 6400001 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := base
			mutate(&p)
			args, err := p.Arguments()
			if !errors.Is(err, ErrUnavailable) || args != nil {
				t.Fatalf("accepted invalid plan: %v %v", args, err)
			}
		})
	}
}

func TestFeatureGapsFailClosed(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		err := CheckFeatures(mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0)
		if (err == nil) != (mask == 0) {
			t.Fatalf("mask %d: %v", mask, err)
		}
	}
}

func FuzzPlan(f *testing.F) {
	f.Add("runtime", uint32(65534), uint32(65534), uint64(0), uint64(1024), uint64(256<<20), uint64(512<<20), uint64(128), uint64(100000))
	f.Add("network", uint32(1), uint32(1), NetworkCapabilities, uint64(32), uint64(1<<20), uint64(32<<20), uint64(8), uint64(1000))
	f.Fuzz(func(t *testing.T, role string, uid, gid uint32, caps, fds, file, memory, tasks, cpu uint64) {
		p := Plan{Role: Role(role), UID: uid, GID: gid, Capabilities: caps, Limits: Limits{fds, file, memory, tasks, cpu}}
		args, err := p.Arguments()
		if err != nil {
			if args != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatal("invalid failure")
			}
			return
		}
		if uid == 0 || gid == 0 || caps&^NetworkCapabilities != 0 || role != string(Network) && caps != 0 {
			t.Fatal("authority expansion")
		}
		again, err := p.Arguments()
		if err != nil || !reflect.DeepEqual(args, again) || len(args) != 10 {
			t.Fatal("unstable ABI")
		}
	})
}
