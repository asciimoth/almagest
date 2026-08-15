package dnsnodes

import "github.com/asciimoth/gonnect/dns"

func dnsTestQuery(name string, typ uint16) *dns.Message {
	return &dns.Message{
		ID:               dns.NextID(),
		Opcode:           dns.OpcodeQuery,
		RecursionDesired: true,
		Questions: []dns.Question{{
			Name:  name,
			Type:  typ,
			Class: dns.ClassIN,
		}},
	}
}
