package debugnodes

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
)

type networkCloseAdapter struct{ gonnect.Network }

func (*networkCloseAdapter) Close() error { return nil }

func TestDebugLogNetworkOperationErrors(t *testing.T) {
	network := &debugLogNetwork{NetworkCloser: &networkCloseAdapter{Network: (gonnect.NativeConfig{}).Build()}}
	ctx := context.Background()
	if _, err := network.Dial(ctx, "invalid", "address"); err == nil {
		t.Fatal("Dial accepted an invalid network")
	}
	if _, err := network.Listen(ctx, "invalid", "address"); err == nil {
		t.Fatal("Listen accepted an invalid network")
	}
	if _, err := network.PacketDial(ctx, "invalid", "address"); err == nil {
		t.Fatal("PacketDial accepted an invalid network")
	}
	if _, err := network.ListenPacket(ctx, "invalid", "address"); err == nil {
		t.Fatal("ListenPacket accepted an invalid network")
	}
	if _, err := network.DialTCP(ctx, "invalid", "", "address"); err == nil {
		t.Fatal("DialTCP accepted an invalid network")
	}
	if _, err := network.ListenTCP(ctx, "invalid", "address"); err == nil {
		t.Fatal("ListenTCP accepted an invalid network")
	}
	if _, err := network.DialUDP(ctx, "invalid", "", "address"); err == nil {
		t.Fatal("DialUDP accepted an invalid network")
	}
	if _, err := network.ListenUDP(ctx, "invalid", "address"); err == nil {
		t.Fatal("ListenUDP accepted an invalid network")
	}
	if _, err := network.ListenPacketConfig(ctx, &gonnect.ListenConfig{}, "invalid", "address"); err == nil {
		t.Fatal("ListenPacketConfig accepted an invalid network")
	}
	if _, err := network.ListenUDPConfig(ctx, &gonnect.ListenConfig{}, "invalid", "address"); err == nil {
		t.Fatal("ListenUDPConfig accepted an invalid network")
	}
	if err := network.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := (&debugLogNetwork{}).Close(); err != nil {
		t.Fatalf("nil network Close error = %v", err)
	}
}

func TestDebugLogTCPAdapters(t *testing.T) {
	base := (gonnect.NativeConfig{}).Build()
	network := &debugLogNetwork{NetworkCloser: &networkCloseAdapter{Network: base}}
	listener, err := network.ListenTCP(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	accepted := make(chan gonnect.TCPConn, 1)
	go func() {
		conn, acceptErr := listener.AcceptTCP()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	client, err := network.DialTCP(context.Background(), "tcp4", "", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server := <-accepted
	defer func() { _ = server.Close() }()
	deadline := time.Now().Add(time.Second)
	_ = client.SetDeadline(deadline)
	_ = server.SetDeadline(deadline)
	if _, err := client.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := server.Read(buf); err != nil || string(buf) != "a" {
		t.Fatalf("server read = %q, %v", buf, err)
	}
	if _, err := client.ReadFrom(bytes.NewBufferString("b")); err != nil {
		t.Fatal(err)
	}
	var copied bytes.Buffer
	if _, err := io.CopyN(&copied, server, 1); err != nil || copied.String() != "b" {
		t.Fatalf("copied = %q, %v", copied.String(), err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := server.CloseRead(); err != nil {
		t.Fatal(err)
	}

	listener2, err := network.ListenTCP(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener2.Close() }()
	go func() {
		conn, acceptErr := base.DialTCP(context.Background(), "tcp4", "", listener2.Addr().String())
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	conn, err := listener2.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestDebugLogUDPAdapters(t *testing.T) {
	base := (gonnect.NativeConfig{}).Build()
	network := &debugLogNetwork{NetworkCloser: &networkCloseAdapter{Network: base}}
	server, err := network.ListenUDP(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	serverAddr := server.LocalAddr().(*net.UDPAddr)
	serverAddrPort := serverAddr.AddrPort()
	client, err := network.DialUDP(context.Background(), "udp4", "127.0.0.1:0", serverAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	deadline := time.Now().Add(time.Second)
	_ = server.SetDeadline(deadline)
	_ = client.SetDeadline(deadline)

	if _, err := client.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if n, _, err := server.ReadFrom(buf); err != nil || string(buf[:n]) != "a" {
		t.Fatalf("ReadFrom = %q, %v", buf[:n], err)
	}
	if _, err := server.WriteToUDP([]byte("b"), client.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	if n, err := client.Read(buf); err != nil || string(buf[:n]) != "b" {
		t.Fatalf("Read = %q, %v", buf[:n], err)
	}
	if _, err := server.WriteToUDPAddrPort([]byte("c"), serverAddrPort); err != nil {
		t.Fatal(err)
	}
	if n, _, err := server.ReadFromUDPAddrPort(buf); err != nil || string(buf[:n]) != "c" {
		t.Fatalf("ReadFromUDPAddrPort = %q, %v", buf[:n], err)
	}
	if _, _, err := server.WriteMsgUDP([]byte("d"), nil, serverAddr); err != nil {
		t.Fatal(err)
	}
	if n, _, _, _, err := server.ReadMsgUDP(buf, nil); err != nil || string(buf[:n]) != "d" {
		t.Fatalf("ReadMsgUDP = %q, %v", buf[:n], err)
	}
	if _, _, err := server.WriteMsgUDPAddrPort([]byte("e"), nil, serverAddrPort); err != nil {
		t.Fatal(err)
	}
	if n, _, _, _, err := server.ReadMsgUDPAddrPort(buf, nil); err != nil || string(buf[:n]) != "e" {
		t.Fatalf("ReadMsgUDPAddrPort = %q, %v", buf[:n], err)
	}
	if _, err := server.WriteToUDP([]byte("g"), serverAddr); err != nil {
		t.Fatal(err)
	}
	if n, _, err := server.ReadFromUDP(buf); err != nil || string(buf[:n]) != "g" {
		t.Fatalf("ReadFromUDP = %q, %v", buf[:n], err)
	}

	packet, err := network.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = packet.Close() }()
	packetAddr := packet.LocalAddr()
	if _, err := server.WriteTo([]byte("f"), packetAddr); err != nil {
		t.Fatal(err)
	}
	_ = packet.SetDeadline(deadline)
	if n, _, err := packet.ReadFrom(buf); err != nil || string(buf[:n]) != "f" {
		t.Fatalf("packet ReadFrom = %q, %v", buf[:n], err)
	}
	if _, err := packet.WriteTo([]byte("h"), packetAddr); err != nil {
		t.Fatal(err)
	}
	if n, err := packet.Read(buf); err != nil || string(buf[:n]) != "h" {
		t.Fatalf("packet Read = %q, %v", buf[:n], err)
	}
	connectedPacket, err := network.PacketDial(context.Background(), "udp4", packetAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connectedPacket.Close() }()
	_ = connectedPacket.SetDeadline(deadline)
	if _, err := connectedPacket.Write([]byte("i")); err != nil {
		t.Fatal(err)
	}
	if n, _, err := packet.ReadFrom(buf); err != nil || string(buf[:n]) != "i" {
		t.Fatalf("connected packet write = %q, %v", buf[:n], err)
	}

	configured, err := network.ListenUDPConfig(context.Background(), &gonnect.ListenConfig{}, "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = configured.Close()
	configuredPacket, err := network.ListenPacketConfig(context.Background(), &gonnect.ListenConfig{}, "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = configuredPacket.Close()
	if got := serverAddrPort.Addr(); got != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("server address = %v", got)
	}
}
