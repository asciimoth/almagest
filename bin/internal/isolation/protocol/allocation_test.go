package protocol

import (
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
)

func TestAllocationRequestContract(t *testing.T) {
	for _, family := range []string{"ipv4", "ipv6"} {
		addr := netip.MustParseAddr("192.0.2.3")
		prefix := netip.MustParsePrefix("192.0.2.3/24")
		bits := uint8(24)
		if family == "ipv6" {
			addr = netip.MustParseAddr("2001:db8::3")
			prefix = netip.MustParsePrefix("2001:db8::3/64")
			bits = 64
		}
		for _, req := range []ReserveAddressRequest{
			{Kind: AddressIP, Family: family},
			{Kind: AddressIP, Family: family, Reserve: true, Address: addr},
			{Kind: AddressSubnet, Family: family, PrefixLength: bits},
			{Kind: AddressSubnet, Family: family, PrefixLength: 0},
			{Kind: AddressSubnet, Family: family, Reserve: true, Subnet: prefix},
		} {
			if err := req.Validate(); err != nil {
				t.Fatalf("valid request=%+v %v", req, err)
			}
		}
	}
	for name, req := range map[string]ReserveAddressRequest{
		"kind":                        {Kind: "route", Family: "ipv4"},
		"family":                      {Kind: AddressIP, Family: "any"},
		"reserve_empty_ip":            {Kind: AddressIP, Family: "ipv4", Reserve: true},
		"reserve_empty_subnet":        {Kind: AddressSubnet, Family: "ipv6", Reserve: true},
		"allocate_ip_with_address":    {Kind: AddressIP, Family: "ipv4", Address: netip.MustParseAddr("192.0.2.1")},
		"allocate_subnet_with_subnet": {Kind: AddressSubnet, Family: "ipv4", Subnet: netip.MustParsePrefix("192.0.2.0/24")},
		"ip_with_subnet":              {Kind: AddressIP, Family: "ipv4", Subnet: netip.MustParsePrefix("192.0.2.0/24")},
		"ip_with_prefix":              {Kind: AddressIP, Family: "ipv4", PrefixLength: 1},
		"subnet_with_address":         {Kind: AddressSubnet, Family: "ipv4", Address: netip.MustParseAddr("192.0.2.1")},
		"reserve_with_prefix":         {Kind: AddressSubnet, Family: "ipv4", Reserve: true, Subnet: netip.MustParsePrefix("192.0.2.0/24"), PrefixLength: 24},
		"wide_ipv4":                   {Kind: AddressSubnet, Family: "ipv4", PrefixLength: 33},
		"wide_ipv6":                   {Kind: AddressSubnet, Family: "ipv6", PrefixLength: 129},
		"address_family":              {Kind: AddressIP, Family: "ipv4", Reserve: true, Address: netip.MustParseAddr("2001:db8::1")},
		"subnet_family":               {Kind: AddressSubnet, Family: "ipv6", Reserve: true, Subnet: netip.MustParsePrefix("192.0.2.0/24")},
		"zone":                        {Kind: AddressIP, Family: "ipv6", Reserve: true, Address: netip.MustParseAddr("fe80::1%host")},
		"mapped_ip":                   {Kind: AddressIP, Family: "ipv6", Reserve: true, Address: netip.MustParseAddr("::ffff:192.0.2.1")},
		"mapped_subnet":               {Kind: AddressSubnet, Family: "ipv6", Reserve: true, Subnet: netip.MustParsePrefix("::ffff:192.0.2.0/120")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := req.Validate(); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("invalid request accepted: %+v %v", req, err)
			}
		})
	}
}

func TestAllocationStateContract(t *testing.T) {
	cases := []struct {
		req   ReserveAddressRequest
		state AllocationState
	}{
		{ReserveAddressRequest{Kind: AddressIP, Family: "ipv4"}, AllocationState{Kind: AddressIP, Address: netip.MustParseAddr("192.0.2.1"), Subnet: netip.MustParsePrefix("192.0.2.0/24")}},
		{ReserveAddressRequest{Kind: AddressIP, Family: "ipv6", Reserve: true, Address: netip.MustParseAddr("2001:db8::1")}, AllocationState{Kind: AddressIP, Address: netip.MustParseAddr("2001:db8::1")}},
		{ReserveAddressRequest{Kind: AddressSubnet, Family: "ipv6", PrefixLength: 64}, AllocationState{Kind: AddressSubnet, Subnet: netip.MustParsePrefix("2001:db8::/64")}},
		{ReserveAddressRequest{Kind: AddressSubnet, Family: "ipv4", Reserve: true, Subnet: netip.MustParsePrefix("192.0.2.3/24")}, AllocationState{Kind: AddressSubnet, Subnet: netip.MustParsePrefix("192.0.2.0/24")}},
	}
	for _, tc := range cases {
		if err := tc.state.Validate(tc.req); err != nil {
			t.Fatalf("valid state: %+v %v", tc.state, err)
		}
		bad := tc.state
		bad.Kind = "foreign"
		if err := bad.Validate(tc.req); err == nil {
			t.Fatal("wrong kind accepted")
		}
	}
	req := cases[0].req
	for _, state := range []AllocationState{
		{Kind: AddressIP},
		{Kind: AddressIP, Address: netip.MustParseAddr("192.0.2.1")},
		{Kind: AddressIP, Address: netip.MustParseAddr("192.0.2.1"), Subnet: netip.MustParsePrefix("198.51.100.0/24")},
		{Kind: AddressIP, Address: netip.MustParseAddr("192.0.2.1"), Subnet: netip.MustParsePrefix("192.0.2.1/24")},
		{Kind: AddressIP, Address: netip.MustParseAddr("2001:db8::1"), Subnet: netip.MustParsePrefix("2001:db8::/64")},
	} {
		if err := state.Validate(req); err == nil {
			t.Fatalf("bad state accepted: %+v", state)
		}
	}
	for _, index := range []int{1, 2, 3} {
		tc := cases[index]
		bad := tc.state
		bad.Address = netip.MustParseAddr("192.0.2.4")
		if err := bad.Validate(tc.req); err == nil {
			t.Fatal("changed address accepted")
		}
		bad = tc.state
		bad.Subnet = netip.MustParsePrefix("2001:db8::/65")
		if err := bad.Validate(tc.req); err == nil {
			t.Fatal("changed subnet accepted")
		}
	}
}

func FuzzAllocationContract(f *testing.F) {
	f.Add(`{"Kind":"ip","Family":"ipv4"}`, `{"Kind":"ip","Address":"192.0.2.1","Subnet":"192.0.2.0/24"}`)
	f.Add(`{"Kind":"subnet","Family":"ipv6","PrefixLength":64}`, `{"Kind":"subnet","Subnet":"2001:db8::/64"}`)
	f.Add(`{"Kind":"ip","Family":"ipv6","Reserve":true,"Address":"fe80::1%host"}`, `{}`)
	f.Fuzz(func(t *testing.T, request, state string) {
		if len(request)+len(state) > MaxControlFrameBytes {
			return
		}
		var req ReserveAddressRequest
		var result AllocationState
		if json.Unmarshal([]byte(request), &req) != nil || json.Unmarshal([]byte(state), &result) != nil {
			return
		}
		if req.Validate() != nil {
			if result.Validate(req) == nil {
				t.Fatal("result accepted for invalid request")
			}
			return
		}
		encoded, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		var again ReserveAddressRequest
		if err := json.Unmarshal(encoded, &again); err != nil || again.Validate() != nil {
			t.Fatal("request validation changed after round trip")
		}
		if result.Validate(req) != nil {
			return
		}
		if result.Kind == AddressIP && req.Reserve && result.Address != req.Address {
			t.Fatal("reservation changed")
		}
		if result.Kind == AddressSubnet && req.Reserve && result.Subnet != req.Subnet.Masked() {
			t.Fatal("reserved subnet changed")
		}
		if result.Kind == AddressSubnet && !req.Reserve && result.Subnet.Bits() != int(req.PrefixLength) {
			t.Fatal("allocated prefix changed")
		}
		if !req.Reserve && result.Kind == AddressIP && !result.Subnet.Contains(result.Address) {
			t.Fatal("address not in parent")
		}
	})
}
