package network

import (
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
	"golang.org/x/sys/unix"
)

// RelayTUNFactory is an explicit broker-only sysnet TUNFactory. Its packet
// reads do not use GSO, so one native read fits the frozen record/queue limits.
// Direct mode must keep its existing factory. Performance qualification must
// compare this path with the unchanged offload-enabled direct baseline.
type RelayTUNFactory struct{}

func (RelayTUNFactory) CreateTUN(_ string, mtu int) (tun.Tun, error) {
	if mtu < 1 || mtu > protocol.MaxPacketRecord {
		return nil, protocol.ErrCapabilityUnsupported
	}
	id, err := protocol.NewResourceID()
	if err != nil {
		return nil, err
	}
	name := "almr-" + id.String()[:10]
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	// Exclusive creation prevents adoption even if the random name collides.
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI | unix.IFF_TUN_EXCL)
	if err = unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "broker-tun")
	device, err := tuntap.CreateTUNFromFile(file, mtu)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return device, nil
}
