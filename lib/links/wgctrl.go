package links

// TypeWGCtrl is the Pasta link type for independently closeable access to a
// shared WireGuard device controller. Controller interface values flow from
// providers to consumers. Interface setting suggestions flow from consumers
// to providers. The WireGuard package owns these payloads and lifecycle
// helpers.
const TypeWGCtrl = "almagest/wgctrl"
