package networknodes

import (
	"encoding/binary"
	"testing"

	"github.com/asciimoth/gonnect/sniffer"
)

func TestSnifferNodeGonnectClassifiersMatchProtocolPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		payloads [][]byte
	}{
		{name: "SSH", payloads: [][]byte{[]byte("SSH-2.0-client\r\n")}},
		{name: "HTTP2", payloads: [][]byte{[]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")}},
		{name: "AMQP", payloads: [][]byte{[]byte("AMQP\x00\x00\x09\x01")}},
		{name: "MQTT", payloads: [][]byte{{0x10, 12, 0, 4, 'M', 'Q', 'T', 'T', 4, 2, 0, 60}}},
		{name: "PostgreSQL", payloads: [][]byte{snifferTestPostgreSQLHeader()}},
		{name: "MongoDB", payloads: [][]byte{snifferTestMongoDBHeader()}},
		{name: "Redis", payloads: [][]byte{[]byte("*2\r\n$3\r\nGET\r\n")}},
		{name: "ProxyProtocolV1", payloads: [][]byte{[]byte("PROXY TCP4 192.0.2.1 198.51.100.2 12345 443\r\n")}},
		{name: "ProxyProtocolV2", payloads: [][]byte{snifferTestProxyProtocolV2Header()}},
		{name: "ProxyProtocol", payloads: [][]byte{
			[]byte("PROXY UNKNOWN\r\n"),
			snifferTestProxyProtocolV2Header(),
		}},
		{name: "SOCKS4", payloads: [][]byte{{4, 1, 0, 80, 127, 0, 0, 1}}},
		{name: "SOCKS5", payloads: [][]byte{{5, 2, 0, 2}}},
		{name: "SOCKS", payloads: [][]byte{
			{4, 1, 0, 80, 127, 0, 0, 1},
			{5, 2, 0, 2},
		}},
		{name: "DNSOverTCP", payloads: [][]byte{snifferTestDNSOverTCPQuery()}},
		{name: "RTSP", payloads: [][]byte{[]byte("DESCRIBE rtsp://example.test/live RTSP/1.0\r\n")}},
		{name: "SIP", payloads: [][]byte{[]byte("INVITE sip:bob@example.test SIP/2.0\r\n")}},
		{name: "STUN", payloads: [][]byte{snifferTestSTUNHeader()}},
		{name: "RDP", payloads: [][]byte{{3, 0, 0, 11, 6, 0xe0, 0, 0, 0, 0, 0}}},
		{name: "SMB", payloads: [][]byte{snifferTestSMB2Header()}},
		{name: "LDAP", payloads: [][]byte{{0x30, 0x0c, 0x02, 0x01, 0x01, 0x60}}},
		{name: "Cassandra", payloads: [][]byte{snifferTestCassandraHeader()}},
		{name: "MemcachedBinary", payloads: [][]byte{snifferTestMemcachedBinaryHeader()}},
		{name: "MemcachedASCII", payloads: [][]byte{[]byte("get alpha\r\n")}},
		{name: "Memcached", payloads: [][]byte{
			snifferTestMemcachedBinaryHeader(),
			[]byte("get alpha\r\n"),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := snifferNodeCompiledClassifier(t, tt.name)
			for payloadIndex, payload := range tt.payloads {
				for split := 0; split <= len(payload); split++ {
					classifier := factory.NewClassifier()
					state := classifier.Feed(payload[:split])
					if state == sniffer.NeedMore && split < len(payload) {
						state = classifier.Feed(payload[split:])
					}
					if state != sniffer.Match {
						t.Fatalf("payload %d split %d final state = %v, want Match", payloadIndex, split, state)
					}
				}
			}
		})
	}
}

func TestSnifferNodeGonnectClassifiersRejectUnrelatedHTTP(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.test\r\n\r\n")
	for _, name := range []string{
		"SSH", "HTTP2", "AMQP", "MQTT", "PostgreSQL", "MongoDB", "Redis",
		"ProxyProtocolV1", "ProxyProtocolV2", "ProxyProtocol", "SOCKS4", "SOCKS5", "SOCKS",
		"DNSOverTCP", "RTSP", "SIP", "STUN", "RDP", "SMB", "LDAP", "Cassandra",
		"MemcachedBinary", "MemcachedASCII", "Memcached",
	} {
		t.Run(name, func(t *testing.T) {
			classifier := snifferNodeCompiledClassifier(t, name).NewClassifier()
			if got := classifier.Feed(payload); got != sniffer.Mismatch {
				t.Fatalf("HTTP payload state = %v, want Mismatch", got)
			}
		})
	}
}

func TestSnifferNodeGonnectClassifierNamesAreCaseSensitive(t *testing.T) {
	for _, name := range []string{"ssh", "http2", "postgresql", "mongodb", "dnsovertcp", "memcached"} {
		t.Run(name, func(t *testing.T) {
			node := &snifferNode{}
			if _, _, err := node.compileRules("SNIFF "+name+"\nSLOT 1", ""); err == nil {
				t.Fatal("compileRules() error = nil, want unknown-classifier error")
			}
		})
	}
}

func snifferNodeCompiledClassifier(t *testing.T, name string) sniffer.Factory {
	t.Helper()
	node := &snifferNode{}
	rules, controls, err := node.compileRules("SNIFF "+name+"\nSLOT 1", "")
	if err != nil {
		t.Fatalf("compileRules(%q): %v", name, err)
	}
	if got, want := len(rules.Classifiers), 1; got != want {
		t.Fatalf("compiled classifier count = %d, want %d", got, want)
	}
	factories := controls.Classifiers()
	if got, want := len(factories), 1; got != want {
		t.Fatalf("control classifier count = %d, want %d", got, want)
	}
	return factories[0]
}

func snifferTestPostgreSQLHeader() []byte {
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[0:4], 8)
	binary.BigEndian.PutUint32(header[4:8], 80877103)
	return header
}

func snifferTestMongoDBHeader() []byte {
	header := make([]byte, 16)
	binary.LittleEndian.PutUint32(header[0:4], 16)
	binary.LittleEndian.PutUint32(header[12:16], 2013)
	return header
}

func snifferTestProxyProtocolV2Header() []byte {
	header := append([]byte(nil), []byte("\r\n\r\n\x00\r\nQUIT\n")...)
	header = append(header, 0x21, 0x11, 0, 12)
	return append(header,
		192, 0, 2, 1,
		198, 51, 100, 2,
		0x30, 0x39,
		0x01, 0xbb,
	)
}

func snifferTestDNSOverTCPQuery() []byte {
	message := []byte{
		0x12, 0x34, 0x01, 0x00,
		0x00, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		4, 't', 'e', 's', 't', 0,
		0, 1, 0, 1,
	}
	query := make([]byte, 2, len(message)+2)
	binary.BigEndian.PutUint16(query, uint16(len(message)))
	return append(query, message...)
}

func snifferTestSTUNHeader() []byte {
	header := make([]byte, 20)
	binary.BigEndian.PutUint16(header[0:2], 0x0001)
	binary.BigEndian.PutUint32(header[4:8], 0x2112a442)
	copy(header[8:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	return header
}

func snifferTestSMB2Header() []byte {
	header := make([]byte, 24)
	binary.BigEndian.PutUint16(header[2:4], 64)
	copy(header[4:8], []byte{0xfe, 'S', 'M', 'B'})
	binary.LittleEndian.PutUint16(header[8:10], 64)
	return header
}

func snifferTestCassandraHeader() []byte {
	header := make([]byte, 9)
	header[0] = 4
	header[4] = 0x05
	return header
}

func snifferTestMemcachedBinaryHeader() []byte {
	header := make([]byte, 24)
	header[0] = 0x80
	binary.BigEndian.PutUint16(header[2:4], 3)
	binary.BigEndian.PutUint32(header[8:12], 3)
	return header
}
