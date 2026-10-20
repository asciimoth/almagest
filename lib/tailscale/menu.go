package tailscalenodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	wgotailscale "github.com/asciimoth/wgo-tailscale"

	"github.com/asciimoth/almagest/lib/nodekit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func (n *tailscaleNode) sendMenuSnapshot(force bool) {
	blocks := make([]formular.Block, 0, 3)
	if block, ok := n.authenticationBlock(); ok {
		blocks = append(blocks, block)
	}
	blocks = append(blocks, n.statusBlock(), n.configBlock())
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, force, blocks...)
}

func (n *tailscaleNode) sendStatusBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.statusBlock())
}

func (n *tailscaleNode) sendConfigBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *tailscaleNode) authenticationBlock() (formular.Block, bool) {
	interaction := n.snapshot.Interaction
	if interaction == nil {
		return formular.Block{}, false
	}
	items := make([]formular.Item, 0, 3)
	if link, ok := interactionMarkdownLink(interaction.URL); ok {
		items = append(items, formular.Item{
			Type:   formular.ItemLabel,
			ID:     "authorizationLink",
			Text:   link,
			Format: formular.TextMarkdown,
		})
	}
	if message := strings.TrimSpace(interaction.Message); message != "" {
		items = append(items, formular.Item{
			Type:   formular.ItemLabel,
			ID:     "message",
			Text:   message,
			Format: formular.TextPlain,
		})
	}
	items = append(items, formular.Item{
		Type:  formular.ItemButton,
		ID:    authResumeID,
		Label: "Check authorization",
	})
	return formular.Block{ID: authBlockID, Order: 0, Generation: 1, Items: items}, true
}

func (n *tailscaleNode) statusBlock() formular.Block {
	snapshot := n.snapshot
	errText := n.formError
	if errText == "" {
		errText = n.runtimeError
	}
	login := snapshot.Client.LoginName
	if snapshot.Client.DisplayName != "" && snapshot.Client.DisplayName != login {
		login = strings.TrimSpace(snapshot.Client.DisplayName + " <" + login + ">")
	}
	selfName := ""
	if snapshot.Self != nil {
		selfName = snapshot.Self.ComputedNameWithHost
		if selfName == "" {
			selfName = snapshot.Self.ComputedName
		}
		if selfName == "" {
			selfName = snapshot.Self.Name
		}
	}
	items := []formular.Item{
		nodekit.ReadonlyTextField("state", "State", n.stateText()),
		nodekit.ReadonlyTextField("controlURL", "Control server", snapshot.Client.ControlURL),
		nodekit.ReadonlyTextField("login", "Login", login),
		nodekit.ReadonlyTextField("tailnet", "Tailnet", snapshot.Domain),
		nodekit.ReadonlyTextField("selfName", "Tailnet name", selfName),
		nodekit.ReadonlyBoolField("machineAuthorized", "Machine authorized", snapshot.Client.MachineAuthorized),
		nodekit.ReadonlyTextField("mapSequence", "Map sequence", strconv.FormatInt(snapshot.Client.MapSequence, 10)),
		nodekit.ReadonlyStringListField("localEndpoints", "Advertised endpoints", localEndpointLines(snapshot.LocalEndpoints)),
		nodekit.ReadonlyStringListField("magicDNSDomains", "MagicDNS search domains", snapshot.DNS.SearchDomains),
		nodekit.ReadonlyStringListField("magicDNSRecords", "MagicDNS records", magicDNSRecordLines(snapshot.DNS.Records)),
		nodekit.ReadonlyStringListField("peers", "Tailnet peers", peerLines(snapshot.Peers)),
		nodekit.ReadonlyMultilineTextField("acl", "ACL packet filter", aclText(snapshot.ACL)),
		nodekit.ReadonlyStringListField("derp", "DERP regions", derpLines(snapshot.DERP)),
		nodekit.ReadonlyStringListField("health", "Health", snapshot.Health),
		nodekit.ReadonlyMultilineTextField("firewall", "Generated firewall policy", n.firewall.PrettyJSONString()),
		nodekit.ReadonlyTextField("error", "Error", errText),
	}
	return formular.Block{ID: "status", Order: 10, Generation: 1, Items: items}
}

func (n *tailscaleNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	enabled := nodekit.ConfigCheckbox(fieldEnabled, "Enabled", n.config.enabled)
	controlURL := nodekit.ConfigTextField(fieldControlURL, "Control URL", n.config.controlURL, statusErr, n.formError, false)
	hostname := nodekit.ConfigTextField(fieldHostname, "Hostname", n.config.hostname, statusErr, n.formError, false)
	authKey := nodekit.ConfigSecretNameField(fieldAuthKeySecret, "Auth key secret", n.config.authKeySecret, statusErr, n.formError)
	ephemeral := nodekit.ConfigCheckbox(fieldEphemeral, "Ephemeral node", n.config.ephemeral)
	listenPort := nodekit.ConfigIntField(fieldListenPort, "Discovery UDP port", n.config.listenPort, statusErr, n.formError)
	disableDERP := nodekit.ConfigCheckbox(fieldDisableDERP, "Disable DERP", n.config.disableDERP)
	disableDiscovery := nodekit.ConfigCheckbox(fieldDisableDiscovery, "Disable discovery", n.config.disableDiscovery)
	return formular.Block{ID: "config", Order: 20, Generation: 1, Form: true, Items: []formular.Item{
		enabled,
		controlURL,
		hostname,
		authKey,
		ephemeral,
		listenPort,
		disableDERP,
		disableDiscovery,
		nodekit.TLSConfigSelectorItem(&n.Base, n.config.tlsConfigName, n.tlsConfigErr),
	}}
}

func (n *tailscaleNode) stateText() string {
	switch {
	case !n.config.enabled:
		return "disabled"
	case n.formError != "":
		return "error"
	case n.ctrlLink == 0 || !wireguardnodes.WGCtrlIsReady(n.controller):
		return "waiting for WireGuard"
	case n.netLink == 0 || !nodekit.NetworkIsReady(n.network):
		return "waiting for Network"
	case n.runtimeError != "":
		return "error"
	case n.pending:
		return "starting"
	case n.snapshot.State == wgotailscale.StateNeedsAuthentication:
		return "needs authentication"
	case n.snapshot.State == wgotailscale.StateRunning && (n.wgApplyPending || !n.wgConfigured):
		return "configuring WireGuard"
	case n.snapshot.State == wgotailscale.StateRunning:
		return "up"
	case n.snapshot.State == wgotailscale.StateDegraded:
		return "degraded"
	case n.snapshot.State == wgotailscale.StateStopped:
		return "stopped"
	default:
		return "starting"
	}
}

func (n *tailscaleNode) updateLabel() error {
	return n.SetLabel(n.stateText())
}

func (n *tailscaleNode) updateInteraction(interaction *wgotailscale.Interaction) {
	if interaction == nil {
		n.clearInteractionPopup()
		return
	}
	if n.W == nil || n.ID == 0 || n.authPopupID != 0 {
		return
	}
	text := interactionText
	if interaction.Kind == wgotailscale.InteractionAuthenticate {
		text = authPopupText
	}
	id, err := n.W.AddNodePopupLocked(n.ID, pasta.NodePopupWard, text, true)
	if err != nil {
		if n.Logger != nil {
			n.Logger.Errf("failed to add Tailscale interaction popup node=%d: %v", n.ID, err)
		}
		return
	}
	n.authPopupID = id
}

func (n *tailscaleNode) clearInteractionPopup() {
	if n.W != nil && n.ID != 0 && n.authPopupID != 0 {
		if err := n.W.RemoveNodePopupLocked(n.ID, n.authPopupID); err != nil && n.Logger != nil {
			n.Logger.Errf("failed to remove Tailscale interaction popup node=%d popup=%d: %v", n.ID, n.authPopupID, err)
		}
	}
	n.authPopupID = 0
}

func (n *tailscaleNode) resumeInteraction() {
	interaction := n.snapshot.Interaction
	if interaction == nil || n.activeRuntimeID == 0 {
		return
	}
	runtimeID := n.activeRuntimeID
	interactionID := interaction.ID
	if err := n.SpawnWorker("Tailscale interaction resume", func() {
		n.runtimeMu.Lock()
		var err error
		if n.runtimeID != runtimeID || n.runtimeClient == nil {
			err = errors.New("tailscale interaction is no longer active")
		} else {
			err = n.runtimeClient.ResumeInteraction(interactionID)
		}
		n.runtimeMu.Unlock()
		if n.W != nil {
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: tailscaleInteractionResult{runtimeID: runtimeID, err: err}})
		}
	}); err != nil {
		n.runtimeError = "resume interaction: " + err.Error()
		n.sendStatusBlock()
		_ = n.updateLabel()
	}
}

func (n *tailscaleNode) handleInteractionResult(result tailscaleInteractionResult) {
	if result.runtimeID != n.activeRuntimeID || result.err == nil {
		return
	}
	n.runtimeError = "resume interaction: " + result.err.Error()
	n.sendStatusBlock()
	_ = n.updateLabel()
}

func interactionMarkdownLink(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	target := strings.ReplaceAll(parsed.String(), "(", "%28")
	target = strings.ReplaceAll(target, ")", "%29")
	return "[Authorize this node](" + target + ")", true
}

func magicDNSRecordLines(records []wgotailscale.DNSRecord) []string {
	out := make([]string, len(records))
	for i, record := range records {
		out[i] = strings.TrimSpace(record.Name) + " " + strings.ToUpper(record.Type) + " " + strings.TrimSpace(record.Value)
	}
	slices.Sort(out)
	return out
}

func peerLines(peers []wgotailscale.PeerInfo) []string {
	out := make([]string, 0, len(peers))
	for _, peer := range peers {
		name := peer.Node.ComputedName
		if name == "" {
			name = peer.Node.Name
		}
		addresses := make([]string, len(peer.Node.Addresses))
		for i, address := range peer.Node.Addresses {
			addresses[i] = address.String()
		}
		endpoints := make([]string, len(peer.Node.Endpoints))
		for i, endpoint := range peer.Node.Endpoints {
			endpoints[i] = endpoint.String()
		}
		online := "unknown"
		if peer.Node.Online != nil {
			online = strconv.FormatBool(*peer.Node.Online)
		}
		line := fmt.Sprintf(
			"%s | %s | endpoints=%s | path=%s | online=%s",
			name,
			strings.Join(addresses, ", "),
			strings.Join(endpoints, ", "),
			peer.Path,
			online,
		)
		if peer.LastError != "" {
			line += " | error=" + peer.LastError
		}
		out = append(out, line)
	}
	slices.Sort(out)
	return out
}

func localEndpointLines(endpoints []wgotailscale.LocalEndpoint) []string {
	out := make([]string, len(endpoints))
	for i, endpoint := range endpoints {
		out[i] = endpoint.Address.String() + " (" + endpoint.Source + ")"
	}
	slices.Sort(out)
	return out
}

func aclText(view wgotailscale.ACLView) string {
	value := struct {
		Revision   uint64                            `json:"revision"`
		Rules      []wgotailscale.ACLRule            `json:"rules,omitempty"`
		NamedRules map[string][]wgotailscale.ACLRule `json:"namedRules,omitempty"`
	}{Revision: view.Revision, Rules: view.Rules, NamedRules: view.NamedRules}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

func derpLines(view wgotailscale.DERPView) []string {
	out := make([]string, 0, len(view.Regions))
	for _, region := range view.Regions {
		marker := ""
		if region.ID == view.Home {
			marker = " (home)"
		}
		latency := ""
		if region.Latency > 0 {
			latency = " | " + region.Latency.Round(time.Millisecond).String() + " via " + string(region.LatencySource)
		}
		out = append(out, fmt.Sprintf("%d %s (%s)%s%s", region.ID, region.Name, region.Code, marker, latency))
	}
	slices.Sort(out)
	return out
}

func (n *tailscaleNode) sendDNSAll() {
	if n.dnsProvider != nil {
		n.dnsOutputs.SendPortLocked(n.W, n.ID, n.dnsp, n.dnsProvider)
	}
}

func (n *tailscaleNode) sendDNSToLink(link uint64) {
	if n.dnsProvider != nil {
		n.dnsOutputs.SendLocked(n.W, n.ID, link, n.dnsProvider)
	}
}

func (n *tailscaleNode) sendFirewallAll() {
	n.ForEachPortLink(n.firewallp, n.sendFirewallToLink)
}

func (n *tailscaleNode) sendFirewallToLink(link uint64) {
	n.EmitIfLinked(link, n.firewall)
}
