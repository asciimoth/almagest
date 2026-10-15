//go:build linux

package roleentry

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/unix"
)

func observeAppliedIsolation(value Bootstrap) (AppliedIsolation, error) {
	if os.Getenv("ALMAGEST_LINUX_PROFILE") != value.ProfileID {
		return AppliedIsolation{}, protocol.ErrInvalidContract
	}
	abi, err := strconv.ParseUint(os.Getenv("ALMAGEST_LANDLOCK_ABI"), 10, 32)
	if err != nil || abi < 3 {
		return AppliedIsolation{}, protocol.ErrInvalidContract
	}
	ruid, euid, suid := unix.Getresuid()
	rgid, egid, sgid := unix.Getresgid()
	fsuid, err := unix.SetfsuidRetUid(-1)
	if err != nil {
		return AppliedIsolation{}, err
	}
	fsgid, err := unix.SetfsgidRetGid(-1)
	if err != nil {
		return AppliedIsolation{}, err
	}
	groups, err := os.Getgroups()
	if err != nil {
		return AppliedIsolation{}, err
	}
	supplementary := make([]uint32, len(groups))
	for index, group := range groups {
		supplementary[index] = uint32(group)
	}
	capabilities, err := observeCapabilitySets()
	if err != nil {
		return AppliedIsolation{}, err
	}
	noNewPrivileges, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		return AppliedIsolation{}, err
	}
	seccompMode, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0)
	if err != nil {
		return AppliedIsolation{}, err
	}
	denialsVerified, err := verifyPolicyDenials(value.ProfileID)
	if err != nil {
		return AppliedIsolation{}, err
	}
	return AppliedIsolation{ProfileID: value.ProfileID, PlanDigest: value.PlanDigest, ProfileRevision: 1, LauncherRevision: 1,
		SeccompRevision: 1, LandlockRevision: 1, LandlockABI: uint32(abi),
		NoNewPrivileges: noNewPrivileges == 1, SeccompMode: uint32(seccompMode), DenialsVerified: denialsVerified,
		Credentials: RuntimeIsolationCredentials{RealUID: uint32(ruid), EffectiveUID: uint32(euid), SavedUID: uint32(suid), FilesystemUID: uint32(fsuid),
			RealGID: uint32(rgid), EffectiveGID: uint32(egid), SavedGID: uint32(sgid), FilesystemGID: uint32(fsgid), SupplementaryGroups: supplementary},
		CapabilitySets: capabilities}, nil
}

func verifyPolicyDenials(profile string) (bool, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err == nil {
		_ = unix.Close(fd)
		return false, errors.New("isolation seccomp policy allowed an internet socket")
	}
	if !errors.Is(err, unix.EPERM) {
		return false, fmt.Errorf("probe seccomp socket denial: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0); !errors.Is(err, unix.EPERM) {
		return false, fmt.Errorf("probe seccomp dumpability denial: %w", err)
	}
	deniedPath := "/blocked"
	if profile == "linux-user-v1" {
		deniedPath = "/proc/self/status"
	}
	file, err := os.Open(deniedPath)
	if err == nil {
		_ = file.Close()
		return false, errors.New("isolation Landlock policy allowed a blocked file read")
	}
	if !errors.Is(err, unix.EACCES) {
		return false, fmt.Errorf("probe Landlock executable denial: %w", err)
	}
	return true, nil
}

func observeCapabilitySets() (RuntimeIsolationCapabilitySets, error) {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return RuntimeIsolationCapabilitySets{}, err
	}
	read := func(low, high uint32) []string {
		values := []string{}
		for bit, name := range isolationCapabilityNames {
			if bit < 32 && low&(1<<bit) != 0 || bit >= 32 && high&(1<<(bit-32)) != 0 {
				values = append(values, name)
			}
		}
		return values
	}
	sets := RuntimeIsolationCapabilitySets{Inheritable: read(data[0].Inheritable, data[1].Inheritable),
		Permitted: read(data[0].Permitted, data[1].Permitted), Effective: read(data[0].Effective, data[1].Effective)}
	for bit, name := range isolationCapabilityNames {
		bounding, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(bit), 0, 0, 0)
		if err != nil {
			return RuntimeIsolationCapabilitySets{}, fmt.Errorf("read capability bounding set: %w", err)
		}
		if bounding == 1 {
			sets.Bounding = append(sets.Bounding, name)
		}
		ambient, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(bit), 0, 0)
		if err != nil {
			return RuntimeIsolationCapabilitySets{}, fmt.Errorf("read ambient capability set: %w", err)
		}
		if ambient == 1 {
			sets.Ambient = append(sets.Ambient, name)
		}
	}
	return sets, nil
}

var isolationCapabilityNames = []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL", "CAP_SETGID", "CAP_SETUID", "CAP_SETPCAP", "CAP_LINUX_IMMUTABLE", "CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST", "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_IPC_LOCK", "CAP_IPC_OWNER", "CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT", "CAP_SYS_PTRACE", "CAP_SYS_PACCT", "CAP_SYS_ADMIN", "CAP_SYS_BOOT", "CAP_SYS_NICE", "CAP_SYS_RESOURCE", "CAP_SYS_TIME", "CAP_SYS_TTY_CONFIG", "CAP_MKNOD", "CAP_LEASE", "CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL", "CAP_SETFCAP", "CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN", "CAP_SYSLOG", "CAP_WAKE_ALARM", "CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ", "CAP_PERFMON", "CAP_BPF", "CAP_CHECKPOINT_RESTORE"}
