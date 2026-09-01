package wireguardnodes

// sendWGCtrl emits a live detached controller API on every WG control link.
// It runs only from workspace callbacks, so wgctrlPayloads needs no mutex.
func (n *wireGuardNode) sendWGCtrl() {
	if n.W == nil || n.ctrlp == 0 {
		return
	}
	n.ForEachPortLink(n.ctrlp, n.sendWGCtrlToLink)
}

// sendWGCtrlToLink preserves a live per-link API. A closed cached API is
// replaced, which lets closable middleware request a fresh controller after it
// changes its active path.
func (n *wireGuardNode) sendWGCtrlToLink(link uint64) {
	if payload, ok := n.wgctrlPayloads[link]; ok && WGCtrlIsReady(payload.API) {
		_ = EmitWGCtrlPayload(n.W, n.ID, link, payload)
		return
	}
	delete(n.wgctrlPayloads, link)
	source := n.controllerSource.Load()
	if !WGCtrlIsReady(source) {
		return
	}
	payload := EmitDetachedWGCtrl(n.W, n.ID, link, source)
	if WGCtrlIsReady(payload.API) {
		n.wgctrlPayloads[link] = payload
	}
}

// refreshWGCtrlToLink emits a new detached API in response to RequestValue.
// Closable middleware closes its old routed payload asynchronously and sends a
// request immediately. Re-emitting the cached payload in that window would
// give the new path an API that is about to close.
//
// The old API is not closed in this workspace callback. Its consumer or the
// middleware owns that operation, and the graph resource remains an idempotent
// fallback for link or node teardown.
func (n *wireGuardNode) refreshWGCtrlToLink(link uint64) {
	source := n.controllerSource.Load()
	if !WGCtrlIsReady(source) {
		return
	}
	payload := EmitDetachedWGCtrl(n.W, n.ID, link, source)
	if WGCtrlIsReady(payload.API) {
		n.wgctrlPayloads[link] = payload
	}
}
