// Package localnetwork constructs the in-process host-network service used by
// direct daemon mode. It owns native platform selection and debug integration
// construction. It does not add a transport or wrap network and TUN data paths.
package localnetwork

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	netstackhelpers "github.com/asciimoth/gonnect-netstack/helpers"
	netstackvtun "github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/subnet"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
	"github.com/kataras/golog"
)

var ErrSystemType = errors.New("system integration type")

const (
	debugDefaultTunBuilderPipe      = "pipe"
	debugDefaultTunBuilderVTunSocks = "vtun-socks"
)

type debugSystemConfig struct {
	NativeOutNetwork              bool
	DisableTun                    bool
	DisableDefaultTun             bool
	DisableDynTun                 bool
	DisableDynDefaultTun          bool
	DisableTunNames               bool
	DisableDefaultTunNames        bool
	DisableStrictMode             bool
	DisableDefaultTunSourceRoutes bool
	DefaultTun                    debugDefaultTunConfig
}

type debugDefaultTunConfig struct {
	Builder        string
	SocksHost      string
	SocksPort      int
	Name           string
	NoLoopbackAddr bool
	EndpointCh     int
	EventCh        int
}

// Build constructs a direct host-network service.
func Build(
	cfg configer.Config,
	commands daemonservices.BackendCallbackDispatcher,
	randomSeed ...int64,
) (daemonservices.HostNetwork, error) {
	return build(cfg, commands, nil, randomSeed...)
}

// BuildWithRuntimeDependencies validates the native dependency
// search on both sides of the daemon's working-directory change. The early
// result prevents a new DLL in file storage from repairing or influencing the
// trusted lookup. Explicit debug integration has no native dependencies.
func BuildWithRuntimeDependencies(
	cfg configer.Config,
	commands daemonservices.BackendCallbackDispatcher,
	beforeChdirErr error,
	randomSeed ...int64,
) (daemonservices.HostNetwork, error) {
	return build(cfg, commands, &beforeChdirErr, randomSeed...)
}

func build(
	cfg configer.Config,
	commands daemonservices.BackendCallbackDispatcher,
	beforeChdirErr *error,
	randomSeed ...int64,
) (daemonservices.HostNetwork, error) {
	view := cfg
	if view != nil {
		typ, err := view.Get(configer.Path{"Type"})
		if err == nil {
			if typ, ok := typ.(string); ok {
				switch typ {
				case "debug":
					golog.Warn("Using debug system integration")
					return BuildDebug(cfg, randomSeed...)
				case "native":
					// noop
				default:
					return nil, ErrSystemType
				}
			}
		}
	}
	if beforeChdirErr != nil {
		if *beforeChdirErr != nil {
			return nil, fmt.Errorf("validate native runtime dependencies before changing directory: %w", *beforeChdirErr)
		}
		if _, err := platform.ValidateRuntimeDependencies(); err != nil {
			return nil, fmt.Errorf("validate native runtime dependencies after changing directory: %w", err)
		}
	}
	system, err := platform.SystemIntegration(view, commands, randomSeed...)
	if err != nil {
		return nil, err
	}
	return withDebugSystemRandomSeed(system, randomSeed), nil
}

// BuildDebug constructs the direct debug host-network service.
func BuildDebug(cfg configer.Config, randomSeed ...int64) (daemonservices.HostNetwork, error) {
	debugCfg := readDebugSystemConfig(cfg)
	localNetwork := gonnect.NativeConfig{}.Build()
	system := &sysnetdebug.System{
		DisableTun:                    debugCfg.DisableTun,
		DisableDefaultTun:             debugCfg.DisableDefaultTun,
		DisableDynTun:                 debugCfg.DisableDynTun,
		DisableDynDefaultTun:          debugCfg.DisableDynDefaultTun,
		DisableTunNames:               debugCfg.DisableTunNames,
		DisableDefaultTunNames:        debugCfg.DisableDefaultTunNames,
		DisableStrictMode:             debugCfg.DisableStrictMode,
		DisableDefaultTunSourceRoutes: debugCfg.DisableDefaultTunSourceRoutes,
		LocalNetwork:                  localNetwork,
	}
	if debugCfg.NativeOutNetwork {
		system.OutNetwork = gonnect.NativeConfig{}.Build()
	}
	switch debugCfg.DefaultTun.Builder {
	case "", debugDefaultTunBuilderVTunSocks:
		system.DefaultTunBuilder = func(opts sysnet.DefaultTunOpts) (tun.Tun, error) {
			return buildDebugDefaultVTunSocks(debugCfg.DefaultTun, localNetwork, opts)
		}
	case debugDefaultTunBuilderPipe:
	default:
		return nil, fmt.Errorf("system debug default tun builder %q", debugCfg.DefaultTun.Builder)
	}
	// TODO: Separated debug admin API for IP packets on Tun emulation and so on
	return withDebugSystemRandomSeed(system, randomSeed), nil
}

type systemWithAllocator struct {
	sysnet.System
	allocator *subnet.CombinedAllocator
}

func (s *systemWithAllocator) AllocIP() subnet.IPAllocator {
	return s.allocator
}

func (s *systemWithAllocator) AllocSubnet() subnet.SubnetAllocator {
	return s.allocator
}

func withDebugSystemRandomSeed(system sysnet.System, randomSeed []int64) sysnet.System {
	if len(randomSeed) == 0 {
		return system
	}
	if _, ok := system.(*sysnetdebug.System); !ok {
		return system
	}
	rng := rand.New(rand.NewSource(randomSeed[0])) //nolint:gosec // The daemon supplies a crypto-random seed.
	return &systemWithAllocator{
		System: system,
		allocator: subnet.NewDefaultAllocator(subnet.DefaultAllocatorConfig{
			Rng: rng,
		}),
	}
}

func readDebugSystemConfig(cfg configer.Config) debugSystemConfig {
	out := debugSystemConfig{
		DefaultTun: debugDefaultTunConfig{
			Builder:        debugDefaultTunBuilderVTunSocks,
			SocksHost:      "127.0.0.1",
			SocksPort:      1081,
			Name:           "debug-default-tun",
			NoLoopbackAddr: true,
			EndpointCh:     1024,
			EventCh:        10,
		},
	}
	view := cfg
	if view == nil {
		return out
	}
	view = view.View(configer.Path{"Debug"})
	if err := configer.Unmarshal(view, &out); err != nil {
		return out
	}
	out.DefaultTun.Builder = strings.TrimSpace(out.DefaultTun.Builder)
	if out.DefaultTun.Builder == "" {
		out.DefaultTun.Builder = debugDefaultTunBuilderVTunSocks
	}
	out.DefaultTun.SocksHost = strings.TrimSpace(out.DefaultTun.SocksHost)
	if out.DefaultTun.SocksHost == "" {
		out.DefaultTun.SocksHost = "127.0.0.1"
	}
	if out.DefaultTun.SocksPort < 0 || out.DefaultTun.SocksPort > 65535 {
		out.DefaultTun.SocksPort = 1081
	}
	out.DefaultTun.Name = strings.TrimSpace(out.DefaultTun.Name)
	if out.DefaultTun.Name == "" {
		out.DefaultTun.Name = "debug-default-tun"
	}
	if out.DefaultTun.EndpointCh <= 0 {
		out.DefaultTun.EndpointCh = 1024
	}
	if out.DefaultTun.EventCh <= 0 {
		out.DefaultTun.EventCh = 10
	}
	return out
}

func buildDebugDefaultVTunSocks(cfg debugDefaultTunConfig, local gonnect.Network, opts sysnet.DefaultTunOpts) (tun.Tun, error) {
	localAddrs, err := debugDefaultTunAddrs(opts.TunAddrs)
	if err != nil {
		return nil, err
	}
	vt, err := (&netstackvtun.Opts{
		Name:           cfg.Name,
		LocalAddrs:     localAddrs,
		NoLoopbackAddr: cfg.NoLoopbackAddr,
		EndpointCh:     cfg.EndpointCh,
		EventCh:        cfg.EventCh,
		NetStackOpts: &netstackhelpers.Opts{
			MTU: opts.MTU,
		},
	}).Build()
	if err != nil {
		return nil, err
	}
	select {
	case <-vt.Events():
	case <-time.After(2 * time.Second):
		_ = vt.Close()
		return nil, errors.New("debug default VTun did not become ready")
	}

	address := net.JoinHostPort(cfg.SocksHost, strconv.Itoa(cfg.SocksPort))
	listener, err := local.Listen(context.Background(), "tcp", address)
	if err != nil {
		_ = vt.Close()
		return nil, err
	}
	golog.Infof("debug DefaultTun SOCKS proxy listening on %s", listener.Addr())

	ctx, cancel := context.WithCancel(context.Background())
	dtun := &debugDefaultVTunSocks{
		Tun:      vt,
		cancel:   cancel,
		listener: listener,
	}
	server := &socksgo.Server{
		Handlers:       debugDefaultTunSocksHandlers(),
		Dialer:         vt.Dial,
		PacketDialer:   vt.PacketDial,
		Listener:       vt.Listen,
		PacketListener: vt.ListenPacket,
		Resolver:       vt,
	}
	go dtun.serve(ctx, server)
	return dtun, nil
}

func debugDefaultTunAddrs(values []string) ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			out = append(out, prefix.Addr())
			continue
		}
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return nil, fmt.Errorf("debug default VTun addr %q: %w", value, err)
		}
		out = append(out, addr)
	}
	return out, nil
}

func debugDefaultTunSocksHandlers() map[protocol.Cmd]socksgo.CommandHandler {
	handlers := make(map[protocol.Cmd]socksgo.CommandHandler)
	for cmd, handler := range socksgo.DefaultCommandHandlers {
		handlers[cmd] = handler
	}
	return handlers
}

type debugDefaultVTunSocks struct {
	tun.Tun

	once     sync.Once
	cancel   context.CancelFunc
	listener net.Listener
}

func (t *debugDefaultVTunSocks) serve(ctx context.Context, server *socksgo.Server) {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				golog.Warnf("debug DefaultTun SOCKS proxy stopped: %v", err)
			}
			return
		}
		go func() {
			if err := server.Accept(ctx, conn, false); err != nil {
				golog.Debugf("debug DefaultTun SOCKS client closed: %v", err)
			}
		}()
	}
}

func (t *debugDefaultVTunSocks) Close() error {
	var err error
	t.once.Do(func() {
		t.cancel()
		err = errors.Join(t.listener.Close(), t.Tun.Close())
	})
	return err
}
