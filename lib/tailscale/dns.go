package tailscalenodes

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
	wgotailscale "github.com/asciimoth/wgo-tailscale"
)

// magicDNSResolver is a live gonnect resolver backed only by the last
// immutable MagicDNS view received from wgo-tailscale. It never falls back to
// the host resolver or sends a DNS packet.
type magicDNSResolver struct {
	mu   sync.RWMutex
	view wgotailscale.DNSView
}

func (r *magicDNSResolver) SetView(view wgotailscale.DNSView) {
	r.mu.Lock()
	r.view = cloneDNSView(view)
	r.mu.Unlock()
}

func (r *magicDNSResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch network {
	case "", "ip", "ip4", "ip6":
	default:
		return nil, magicDNSError(host, "unsupported network")
	}
	view := r.snapshot()
	records := dnsRecordsByName(view.Records)
	for _, name := range magicDNSLookupNames(host, view.SearchDomains) {
		addresses := resolveMagicDNSName(records, name, network, make(map[string]bool))
		slices.SortFunc(addresses, func(a, b netip.Addr) int { return a.Compare(b) })
		addresses = slices.Compact(addresses)
		if len(addresses) != 0 {
			return addresses, nil
		}
	}
	return nil, magicDNSNotFound(host)
}

func (r *magicDNSResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	addresses, err := r.LookupNetIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, len(addresses))
	for i, address := range addresses {
		out[i] = append(net.IP(nil), address.AsSlice()...)
	}
	return out, nil
}

func (r *magicDNSResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	addresses, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IPAddr, len(addresses))
	for i, address := range addresses {
		out[i] = net.IPAddr{IP: append(net.IP(nil), address.AsSlice()...)}
	}
	return out, nil
}

func (r *magicDNSResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	addresses, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(addresses))
	for i, address := range addresses {
		out[i] = address.String()
	}
	return out, nil
}

func (r *magicDNSResolver) LookupAddr(ctx context.Context, address string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	want, err := netip.ParseAddr(strings.TrimSpace(address))
	if err != nil {
		return nil, magicDNSError(address, "invalid address")
	}
	var names []string
	for _, record := range r.snapshot().Records {
		got, parseErr := netip.ParseAddr(strings.TrimSpace(record.Value))
		if parseErr == nil && got.Unmap() == want.Unmap() {
			names = append(names, absoluteDNSName(record.Name))
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) == 0 {
		return nil, magicDNSNotFound(address)
	}
	return names, nil
}

func (r *magicDNSResolver) LookupCNAME(ctx context.Context, host string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	view := r.snapshot()
	records := dnsRecordsByName(view.Records)
	for _, name := range magicDNSLookupNames(host, view.SearchDomains) {
		for _, record := range records[name] {
			if strings.EqualFold(record.Type, "CNAME") {
				return absoluteDNSName(record.Value), nil
			}
		}
		if len(resolveMagicDNSName(records, name, "ip", make(map[string]bool))) != 0 {
			return absoluteDNSName(name), nil
		}
	}
	return "", magicDNSNotFound(host)
}

func (*magicDNSResolver) LookupPort(_ context.Context, network, service string) (int, error) {
	return gonnect.LookupPortOffline(network, service)
}

func (r *magicDNSResolver) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	records, err := r.lookupTyped(ctx, name, "NS")
	if err != nil {
		return nil, err
	}
	out := make([]*net.NS, 0, len(records))
	for _, record := range records {
		out = append(out, &net.NS{Host: absoluteDNSName(record.Value)})
	}
	return out, nil
}

func (r *magicDNSResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	records, err := r.lookupTyped(ctx, name, "MX")
	if err != nil {
		return nil, err
	}
	out := make([]*net.MX, 0, len(records))
	for _, record := range records {
		parts := strings.Fields(record.Value)
		if len(parts) != 2 {
			continue
		}
		preference, parseErr := strconv.ParseUint(parts[0], 10, 16)
		if parseErr == nil {
			out = append(out, &net.MX{Pref: uint16(preference), Host: absoluteDNSName(parts[1])})
		}
	}
	if len(out) == 0 {
		return nil, magicDNSNotFound(name)
	}
	return out, nil
}

func (r *magicDNSResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	query := strings.TrimSpace(name)
	if service != "" {
		query = "_" + strings.TrimPrefix(service, "_") + "._" + strings.TrimPrefix(proto, "_") + "." + query
	}
	records, err := r.lookupTyped(ctx, query, "SRV")
	if err != nil {
		return "", nil, err
	}
	out := make([]*net.SRV, 0, len(records))
	for _, record := range records {
		parts := strings.Fields(record.Value)
		if len(parts) != 4 {
			continue
		}
		priority, priorityErr := strconv.ParseUint(parts[0], 10, 16)
		weight, weightErr := strconv.ParseUint(parts[1], 10, 16)
		port, portErr := strconv.ParseUint(parts[2], 10, 16)
		if priorityErr == nil && weightErr == nil && portErr == nil {
			out = append(out, &net.SRV{
				Target: absoluteDNSName(parts[3]), Priority: uint16(priority),
				Weight: uint16(weight), Port: uint16(port),
			})
		}
	}
	if len(out) == 0 {
		return "", nil, magicDNSNotFound(query)
	}
	return absoluteDNSName(query), out, nil
}

func (r *magicDNSResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	records, err := r.lookupTyped(ctx, name, "TXT")
	if err != nil {
		return nil, err
	}
	out := make([]string, len(records))
	for i, record := range records {
		out[i] = record.Value
	}
	return out, nil
}

func (r *magicDNSResolver) lookupTyped(ctx context.Context, host, recordType string) ([]wgotailscale.DNSRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view := r.snapshot()
	records := dnsRecordsByName(view.Records)
	for _, name := range magicDNSLookupNames(host, view.SearchDomains) {
		var out []wgotailscale.DNSRecord
		for _, record := range records[name] {
			if strings.EqualFold(record.Type, recordType) {
				out = append(out, record)
			}
		}
		if len(out) != 0 {
			return out, nil
		}
	}
	return nil, magicDNSNotFound(host)
}

func (r *magicDNSResolver) snapshot() wgotailscale.DNSView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneDNSView(r.view)
}

func cloneDNSView(view wgotailscale.DNSView) wgotailscale.DNSView {
	view.SearchDomains = slices.Clone(view.SearchDomains)
	view.CertDomains = slices.Clone(view.CertDomains)
	view.Nameservers = slices.Clone(view.Nameservers)
	view.Records = slices.Clone(view.Records)
	return view
}

func dnsRecordsByName(records []wgotailscale.DNSRecord) map[string][]wgotailscale.DNSRecord {
	out := make(map[string][]wgotailscale.DNSRecord)
	for _, record := range records {
		name := normalizeDNSName(record.Name)
		out[name] = append(out[name], record)
	}
	return out
}

func magicDNSLookupNames(host string, searchDomains []string) []string {
	host = strings.TrimSpace(host)
	names := []string{normalizeDNSName(host)}
	if !strings.HasSuffix(host, ".") && !strings.Contains(host, ".") {
		for _, domain := range searchDomains {
			names = append(names, normalizeDNSName(host+"."+domain))
		}
	}
	return slices.Compact(names)
}

func resolveMagicDNSName(records map[string][]wgotailscale.DNSRecord, name, network string, seen map[string]bool) []netip.Addr {
	if seen[name] {
		return nil
	}
	seen[name] = true
	var out []netip.Addr
	for _, record := range records[name] {
		switch strings.ToUpper(record.Type) {
		case "A", "AAAA":
			address, err := netip.ParseAddr(strings.TrimSpace(record.Value))
			if err != nil || network == "ip4" && !address.Is4() || network == "ip6" && !address.Is6() {
				continue
			}
			out = append(out, address.Unmap())
		case "CNAME":
			out = append(out, resolveMagicDNSName(records, normalizeDNSName(record.Value), network, seen)...)
		}
	}
	return out
}

func normalizeDNSName(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

func absoluteDNSName(value string) string {
	value = normalizeDNSName(value)
	if value == "" {
		return ""
	}
	return value + "."
}

func magicDNSError(name, message string) error {
	return &net.DNSError{Err: message, Name: name}
}

func magicDNSNotFound(name string) error {
	return &net.DNSError{Err: "no such MagicDNS host", Name: name, IsNotFound: true}
}
