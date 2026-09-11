package tunnodes

import (
	"errors"
	"os"
	"slices"

	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
)

func (n *defaultTunNode) configureDefaultTun() error {
	opts := n.effectiveDefaultTunOpts()
	if n.dtun != nil && n.hasEffective && defaultTunOptsEqual(opts, n.effective) {
		_ = n.updateLabel()
		return nil
	}
	if n.dtun != nil && n.dynDefaultTun && n.hasEffective && defaultTunOptsDynamicallyCompatible(n.effective, opts) {
		if err := n.applyDynamicDefaultTunOpts(opts); err == nil {
			n.effective = opts.Copy()
			n.hasEffective = true
			n.formError = ""
			_ = n.updateLabel()
			n.sendEffectiveBlock()
			n.refreshDefaultTunWarningsLocked()
			return nil
		} else if n.Logger != nil {
			n.Logger.Warnf("DefaultTun dynamic configuration failed; rebuilding: %v", err)
		}
	}
	if err := n.rebuildDefaultTun(opts); err != nil {
		n.logConfigureDefaultTunError(err)
		return err
	}
	return nil
}

func (n *defaultTunNode) logConfigureDefaultTunError(err error) {
	if err == nil || n.Logger == nil {
		return
	}
	n.Logger.Errf("configure default TUN failed: %v", err)
}

func (n *defaultTunNode) rebuildDefaultTun(opts sysnet.DefaultTunOpts) error {
	if n.System() == nil {
		return errors.New("no sysnet system")
	}
	if err := n.verifyDefaultTunOpts(opts); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	previous := n.dtun
	// A stable DefaultTun wrapper can change its native source during the
	// build. Capture source identity before the call so the old and new states
	// do not become indistinguishable through the same mutable object.
	previousSource := captureDefaultTunSource(previous)
	dtun, err := n.System().BuildDefaultTun(opts.Copy())
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	n.formError = ""
	n.dtun = dtun
	n.effective = opts.Copy()
	n.hasEffective = true
	_ = n.W.AddNodeResourceLocked(n.ID, dtun)
	n.applyDefaultTunDNS()
	_ = n.updateLabel()
	n.sendEffectiveBlock()
	n.refreshDefaultTunWarningsLocked()
	nativeUnchanged := defaultTunSourceUnchanged(previousSource, dtun)
	samePublicTun := SameTunInstance(previous, dtun)
	if previous == nil && n.Logger != nil {
		mtu, mtuErr := dtun.MTU()
		n.Logger.Infof(
			"DefaultTun native source ready: generation=%d generation_available=%t file=%p mtu=%d mtu_error=%v mro=%d mwo=%d batch=%d",
			defaultTunSourceGenerationOf(dtun),
			defaultTunHasSourceGeneration(dtun),
			defaultTunNativeFile(dtun),
			mtu,
			mtuErr,
			dtun.MRO(),
			dtun.MWO(),
			dtun.BatchSize(),
		)
	} else if !nativeUnchanged && n.Logger != nil {
		n.Logger.Infof(
			"DefaultTun native source replaced: previous_generation=%d current_generation=%d previous_file=%p current_file=%p generation_available=%t mro=%d mwo=%d batch=%d",
			previousSource.generation,
			defaultTunSourceGenerationOf(dtun),
			previousSource.nativeFile,
			defaultTunNativeFile(dtun),
			previousSource.hasGeneration && defaultTunHasSourceGeneration(dtun),
			dtun.MRO(),
			dtun.MWO(),
			dtun.BatchSize(),
		)
	}
	if nativeUnchanged {
		// Some systems return a new public wrapper for the same source. Keep the
		// emitted detached wrappers and update only their cache source identity.
		// A system with a stable public wrapper needs no rebind or close.
		if !samePublicTun {
			n.outputTuns.RebindSource(previous, dtun)
			_ = previous.Close()
		}
		return nil
	}
	n.outputTuns.CloseAll()
	if previous != nil && !samePublicTun {
		_ = previous.Close()
	}
	n.outputTuns.Wait()
	n.sendAll()
	return nil
}

type defaultTunSourceGeneration interface {
	SourceGeneration() uint64
}

type defaultTunSourceSnapshot struct {
	nativeFile    *os.File
	generation    uint64
	hasGeneration bool
}

func captureDefaultTunSource(t sysnet.DefaultTun) defaultTunSourceSnapshot {
	if t == nil {
		return defaultTunSourceSnapshot{}
	}
	generation, hasGeneration := t.(defaultTunSourceGeneration)
	snapshot := defaultTunSourceSnapshot{
		nativeFile:    defaultTunNativeFile(t),
		hasGeneration: hasGeneration,
	}
	if hasGeneration {
		snapshot.generation = generation.SourceGeneration()
	}
	return snapshot
}

func defaultTunSourceUnchanged(previous defaultTunSourceSnapshot, current sysnet.DefaultTun) bool {
	if current == nil {
		return false
	}
	if generation, ok := current.(defaultTunSourceGeneration); previous.hasGeneration && ok {
		return generation.SourceGeneration() == previous.generation
	}
	return previous.nativeFile != nil && previous.nativeFile == defaultTunNativeFile(current)
}

func defaultTunSourceGenerationOf(t sysnet.DefaultTun) uint64 {
	if generation, ok := t.(defaultTunSourceGeneration); ok {
		return generation.SourceGeneration()
	}
	return 0
}

func defaultTunHasSourceGeneration(t sysnet.DefaultTun) bool {
	_, ok := t.(defaultTunSourceGeneration)
	return ok
}

func defaultTunNativeFile(t sysnet.DefaultTun) *os.File {
	if t == nil {
		return nil
	}
	return t.File()
}

func (n *defaultTunNode) refreshDefaultTunWarningsLocked() {
	if n.W == nil {
		n.warningPopupIDs = nil
		return
	}
	for _, id := range n.warningPopupIDs {
		if err := n.W.RemoveNodePopupLocked(n.ID, id); err != nil && n.Logger != nil {
			n.Logger.Errf("failed to remove DefaultTun warning popup node=%d popup=%d: %v", n.ID, id, err)
		}
	}
	n.warningPopupIDs = nil

	if n.System() == nil || n.dtun == nil {
		return
	}
	for _, warning := range n.System().DefaultTunWarnings(n.dtun) {
		id, err := n.W.AddNodePopupLocked(n.ID, pasta.NodePopupWard, string(warning), true)
		if err != nil {
			if n.Logger != nil {
				n.Logger.Errf("failed to add DefaultTun warning popup node=%d warning=%q: %v", n.ID, warning, err)
			}
			continue
		}
		n.warningPopupIDs = append(n.warningPopupIDs, id)
	}
}

// applyDefaultTunDNS installs the DNS provider currently connected to the
// DefaultTun DNS port. A missing or no-longer-ready provider deliberately
// installs nil so sysnet's local DefaultTun DNS server drops requests instead
// of silently falling back to the system OutDNS provider.
func (n *defaultTunNode) applyDefaultTunDNS() {
	if n.dtun == nil {
		return
	}
	if dnsnodes.DNSIsReady(n.dnsProvider) {
		n.dtun.SetDns(n.dnsProvider)
		return
	}
	n.dtun.SetDns(nil)
}

func (n *defaultTunNode) applyDynamicDefaultTunOpts(opts sysnet.DefaultTunOpts) error {
	if n.System() == nil || n.dtun == nil {
		return errors.New("no default tun")
	}
	if !slices.Equal(n.effective.TunAddrs, opts.TunAddrs) {
		if err := n.System().SetTunAddrs(n.dtun, slices.Clone(opts.TunAddrs)); err != nil {
			return err
		}
	}
	if n.effective.MTU != opts.MTU {
		if err := n.System().SetTunMTU(n.dtun, opts.MTU); err != nil {
			return err
		}
	}
	return nil
}

func (n *defaultTunNode) effectiveDefaultTunOpts() sysnet.DefaultTunOpts {
	opts := n.config.defaultTunOpts()
	portCfg := n.portCfg.Merge()
	opts.TunAddrs = mergeDefaultTunStringLists(opts.TunAddrs, portCfg.Addrs)
	opts.TunRoutes = mergeDefaultTunStringLists(opts.TunRoutes, portCfg.Routes)
	opts.SourceRoutes = defaultTunSourceRoutes(portCfg.SourceRoutes)
	opts.MTU = mergeDefaultTunMTU(opts.MTU, portCfg.MTU)
	return opts
}

func defaultTunSourceRoutes(routes []TunSourceRoute) []sysnet.TunSourceRoute {
	if routes == nil {
		return nil
	}
	out := make([]sysnet.TunSourceRoute, 0, len(routes))
	for _, route := range routes {
		out = append(out, sysnet.TunSourceRoute{
			Destination: route.Destination,
			Source:      route.Source,
		})
	}
	return out
}

func (n *defaultTunNode) verifyDefaultTunOpts(opts sysnet.DefaultTunOpts) error {
	if n.System() == nil {
		return errors.New("no sysnet system")
	}
	return n.System().VerifyDefaultTunOpts(opts.Copy())
}

func (n *defaultTunNode) sendAll() {
	if n.dtun == nil {
		return
	}
	n.ForEachPortLink(n.tunp, n.sendTunToLink)
}

func (n *defaultTunNode) sendTunToLink(link uint64) {
	if n.dtun == nil {
		return
	}
	n.outputTuns.SendLocked(n.W, n.ID, link, n.dtun, enginePool(n.Runtime))
}

func (n *defaultTunNode) refreshTunToLink(link uint64) {
	if n.dtun == nil {
		return
	}
	// Reuse a ready detached wrapper. Replacing one that is blocked in a
	// native TUN read leaves two readers on the same device until the old read
	// returns. The old reader can then consume and discard the next packet.
	n.outputTuns.SendLocked(n.W, n.ID, link, n.dtun, enginePool(n.Runtime))
}
