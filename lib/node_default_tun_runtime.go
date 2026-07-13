package almagest

import (
	"errors"
	"slices"

	"github.com/asciimoth/gonnect/sysnet"
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
			return nil
		}
	}
	if err := n.rebuildDefaultTun(opts); err != nil {
		n.logConfigureDefaultTunError(err)
		return err
	}
	return nil
}

func (n *defaultTunNode) logConfigureDefaultTunError(err error) {
	if err == nil || n.logger == nil {
		return
	}
	n.logger.Errf("configure default TUN failed: %v", err)
}

func (n *defaultTunNode) rebuildDefaultTun(opts sysnet.DefaultTunOpts) error {
	if n.engine == nil || n.engine.sys == nil {
		return errors.New("no sysnet system")
	}
	if err := n.verifyDefaultTunOpts(opts); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	previous := n.dtun
	dtun, err := n.engine.sys.BuildDefaultTun(opts.Copy())
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	if outDNS := n.engine.sys.OutDNS(); outDNS != nil {
		dtun.SetDns(outDNS)
	}
	n.formError = ""
	n.dtun = dtun
	n.effective = opts.Copy()
	n.hasEffective = true
	_ = n.w.AddNodeResourceLocked(n.id, dtun)
	_ = n.updateLabel()
	n.sendEffectiveBlock()
	n.outputTuns.CloseAll()
	n.sendAll()
	if previous != nil {
		_ = previous.Close()
	}
	n.outputTuns.Wait()
	return nil
}

func (n *defaultTunNode) applyDynamicDefaultTunOpts(opts sysnet.DefaultTunOpts) error {
	if n.engine == nil || n.engine.sys == nil || n.dtun == nil {
		return errors.New("no default tun")
	}
	if !slices.Equal(n.effective.TunAddrs, opts.TunAddrs) {
		if err := n.engine.sys.SetTunAddrs(n.dtun, slices.Clone(opts.TunAddrs)); err != nil {
			return err
		}
	}
	if n.effective.MTU != opts.MTU {
		if err := n.engine.sys.SetTunMTU(n.dtun, opts.MTU); err != nil {
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
	opts.MTU = mergeDefaultTunMTU(opts.MTU, portCfg.MTU)
	return opts
}

func (n *defaultTunNode) verifyDefaultTunOpts(opts sysnet.DefaultTunOpts) error {
	if n.engine == nil || n.engine.sys == nil {
		return errors.New("no sysnet system")
	}
	return n.engine.sys.VerifyDefaultTunOpts(opts.Copy())
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
	n.outputTuns.SendLocked(n.w, n.id, link, n.dtun, enginePool(n.engine))
}

func (n *defaultTunNode) refreshTunToLink(link uint64) {
	if n.dtun == nil {
		return
	}
	n.outputTuns.RefreshLocked(n.w, n.id, link, n.dtun, enginePool(n.engine))
}
