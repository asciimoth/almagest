package httpnodes

import (
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

func (n *fetchAddrNode) updateFetchAddrLabel() error {
	return n.SetLabel(n.statusText)
}

func (n *fetchAddrNode) sendFetchAddrMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.fetchAddrCheckBlock(), n.fetchAddrResultBlock())
}

func (n *fetchAddrNode) sendFetchAddrCheckBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.fetchAddrCheckBlock())
}

func (n *fetchAddrNode) sendFetchAddrResultBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.fetchAddrResultBlock())
}

func (n *fetchAddrNode) fetchAddrCheckBlock() formular.Block {
	providerNames := make([]any, 0, len(n.providers))
	for _, provider := range n.providers {
		providerNames = append(providerNames, provider.name)
	}
	return formular.Block{ID: "check", Order: 10, Generation: 1, Items: []formular.Item{
		formular.TextField("provider", "Provider", n.provider, formular.AllowedValues(providerNames...)),
		nodeTLSConfigSelectorItem(&n.NodeBase, n.tlsConfigName, n.tlsConfigErr),
		{
			Type:     formular.ItemButton,
			ID:       "check",
			Label:    "Check",
			Inactive: n.pending,
		},
	}}
}

func (n *fetchAddrNode) fetchAddrResultBlock() formular.Block {
	return formular.Block{ID: "result", Order: 20, Generation: 1, Items: []formular.Item{
		formular.PlainLabel("status", "Status: "+n.statusText),
		formular.ReadonlyTextField("error", "Error", n.errText),
		formular.ReadonlyTextField("publicIP", "Public IP", n.data.PublicIP),
		formular.ReadonlyTextField("countryCode", "Country code", n.data.CountryCode),
		formular.ReadonlyTextField("city", "City", n.data.City),
		formular.FloatField("latitude", "Latitude", n.data.Latitude, formular.Readonly),
		formular.FloatField("longitude", "Longitude", n.data.Longitude, formular.Readonly),
	}}
}

func (n *fetchAddrNode) emitAllFetchAddrData() {
	n.ForEachPortLink(n.publicIPOut, func(link uint64) { n.emitFetchAddrDataToLink(n.publicIPOut, link) })
	n.ForEachPortLink(n.countryOut, func(link uint64) { n.emitFetchAddrDataToLink(n.countryOut, link) })
	n.ForEachPortLink(n.cityOut, func(link uint64) { n.emitFetchAddrDataToLink(n.cityOut, link) })
	n.ForEachPortLink(n.latitudeOut, func(link uint64) { n.emitFetchAddrDataToLink(n.latitudeOut, link) })
	n.ForEachPortLink(n.longitudeOut, func(link uint64) { n.emitFetchAddrDataToLink(n.longitudeOut, link) })
}

func (n *fetchAddrNode) emitFetchAddrDataToLink(port, link uint64) {
	var payload any
	switch port {
	case n.publicIPOut:
		payload = std.String(n.data.PublicIP)
	case n.countryOut:
		payload = std.String(n.data.CountryCode)
	case n.cityOut:
		payload = std.String(n.data.City)
	case n.latitudeOut:
		payload = std.Float(n.data.Latitude)
	case n.longitudeOut:
		payload = std.Float(n.data.Longitude)
	default:
		return
	}
	n.EmitIfLinked(link, payload)
}

func (n *fetchAddrNode) emitFetchAddrTrigger(port uint64) {
	n.ForEachPortLink(port, func(link uint64) {
		n.EmitIfLinked(link, std.Trigger{})
	})
}
