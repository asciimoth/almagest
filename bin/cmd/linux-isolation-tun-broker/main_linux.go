//go:build linux

// linux-isolation-tun-broker owns the real native TUN for the checkpoint 12
// restricted-runtime experiment. It is not linked to almagestd.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/asciimoth/gonnect/sysnet"
	gtun "github.com/asciimoth/gonnect/tun"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"golang.org/x/sys/unix"
)

const tunFrameHeader = 10

type tunPath struct {
	device      gtun.Tun
	connection  *net.UDPConn
	headerBytes int
}

func checksum(data []byte, initial uint32) uint16 {
	sum := initial
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + sum>>16
	}
	return ^uint16(sum)
}

func reversePacket(packet []byte) error {
	if len(packet) < 28 {
		return errors.New("short TUN packet")
	}
	switch packet[0] >> 4 {
	case 4:
		header := int(packet[0]&0x0f) * 4
		if header < 20 || len(packet) < header+8 || packet[9] != unix.IPPROTO_UDP {
			return errors.New("invalid IPv4 UDP packet")
		}
		for index := range 4 {
			packet[12+index], packet[16+index] = packet[16+index], packet[12+index]
		}
		packet[10], packet[11] = 0, 0
		binary.BigEndian.PutUint16(packet[10:12], checksum(packet[:header], 0))
		packet[header], packet[header+2] = packet[header+2], packet[header]
		packet[header+1], packet[header+3] = packet[header+3], packet[header+1]
		packet[header+6], packet[header+7] = 0, 0 // IPv4 permits a zero UDP checksum.
	case 6:
		if len(packet) < 48 || packet[6] != unix.IPPROTO_UDP {
			return errors.New("invalid IPv6 UDP packet")
		}
		for index := range 16 {
			packet[8+index], packet[24+index] = packet[24+index], packet[8+index]
		}
		packet[40], packet[42] = packet[42], packet[40]
		packet[41], packet[43] = packet[43], packet[41]
		packet[46], packet[47] = 0, 0
		udpLength := uint32(len(packet) - 40)
		pseudo := uint32(udpLength>>16) + uint32(udpLength&0xffff) + uint32(unix.IPPROTO_UDP)
		for offset := 8; offset < 40; offset += 2 {
			pseudo += uint32(binary.BigEndian.Uint16(packet[offset : offset+2]))
		}
		value := checksum(packet[40:], pseudo)
		if value == 0 {
			value = 0xffff
		}
		binary.BigEndian.PutUint16(packet[46:48], value)
	default:
		return errors.New("unknown TUN packet family")
	}
	return nil
}

func (p tunPath) roundTrip(payload []byte) error {
	if _, err := p.connection.Write(payload); err != nil {
		return err
	}
	batch := p.device.BatchSize()
	buffers, sizes := make([][]byte, batch), make([]int, batch)
	for index := range buffers {
		buffers[index] = make([]byte, 65536+p.device.MRO())
	}
	var packet []byte
	for packet == nil {
		count, err := p.device.Read(buffers, sizes, p.device.MRO())
		if err != nil {
			return err
		}
		if count < 1 {
			return errors.New("native TUN returned an empty batch")
		}
		for index := range count {
			candidate := buffers[index][p.device.MRO() : p.device.MRO()+sizes[index]]
			if len(candidate) >= 20 && ((candidate[0]>>4 == 4 && candidate[9] == unix.IPPROTO_UDP) ||
				(len(candidate) >= 40 && candidate[0]>>4 == 6 && candidate[6] == unix.IPPROTO_UDP)) {
				packet = candidate
				break
			}
		}
	}
	if err := reversePacket(packet); err != nil {
		return err
	}
	output := make([]byte, p.device.MWO()+len(packet))
	copy(output[p.device.MWO():], packet)
	if _, err := p.device.Write([][]byte{output}, p.device.MWO()); err != nil {
		return err
	}
	if err := p.connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	read, err := p.connection.Read(received)
	if err != nil {
		return err
	}
	if read != len(payload) {
		return fmt.Errorf("TUN UDP response length %d", read)
	}
	for index := range payload {
		if received[index] != payload[index] {
			return errors.New("TUN payload changed")
		}
	}
	return nil
}

func (p tunPath) directRoundTrip(payload []byte) error {
	if _, err := p.connection.Write(payload); err != nil {
		return err
	}
	if err := p.connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	read, err := p.connection.Read(received)
	if err != nil {
		return err
	}
	if read != len(payload) {
		return fmt.Errorf("direct TUN response length %d", read)
	}
	for index := range payload {
		if received[index] != payload[index] {
			return errors.New("direct TUN payload changed")
		}
	}
	return nil
}

func writeU64(value []byte, number uint64) { binary.BigEndian.PutUint64(value, number) }

func runSharedTun(path tunPath, channel int, header []byte, batch, packetBytes, udpBytes, iterations int) error {
	memory, err := unix.MemfdCreate("almagest-tun-data-path", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(memory) }()
	if err = unix.Ftruncate(memory, 65536); err != nil {
		return err
	}
	mapping, err := unix.Mmap(memory, 0, 65536, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Munmap(mapping) }()
	request, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(request) }()
	response, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(response) }()
	if err = unix.Sendmsg(channel, header[:10], unix.UnixRights(memory, request, response), nil, 0); err != nil {
		return err
	}
	signal := make([]byte, 8)
	for range iterations {
		if _, err = unix.Read(request, signal); err != nil {
			return err
		}
		length := int(binary.LittleEndian.Uint32(mapping[:4]))
		if length != batch*packetBytes || length > len(mapping)-4 {
			return errors.New("invalid shared TUN record length")
		}
		for packet := range batch {
			start := 4 + packet*packetBytes
			if err = path.roundTrip(mapping[start : start+udpBytes]); err != nil {
				return err
			}
		}
		binary.LittleEndian.PutUint64(signal, 1)
		if _, err = unix.Write(response, signal); err != nil {
			return err
		}
	}
	return nil
}

func run() error {
	system, err := linuxsysnet.New(linuxsysnet.SystemConfig{DNS: linuxsysnet.DNSConfig{Mode: linuxsysnet.DNSModeDisabled}})
	if err != nil {
		return err
	}
	defer func() { _ = system.Close() }()
	device, err := system.BuildTun(sysnet.TunOpts{Name: "alm-data-path", MTU: 1500})
	if err != nil {
		return err
	}
	defer func() { _ = device.Close() }()
	if err = system.SetTunAddrs(device, []string{"198.18.42.1/30", "fd00:616c:6d61::1/126"}); err != nil {
		return err
	}
	path4, err := makeTunPath(device, "udp4", "198.18.42.1", "198.18.42.2", 28)
	if err != nil {
		return err
	}
	defer func() { _ = path4.connection.Close() }()
	path6, err := makeTunPath(device, "udp6", "fd00:616c:6d61::1", "fd00:616c:6d61::2", 48)
	if err != nil {
		addrs, inspectErr := system.GetTunAddrs(device)
		return fmt.Errorf("create IPv6 TUN path (addresses %v, inspect error %v): %w", addrs, inspectErr, err)
	}
	defer func() { _ = path6.connection.Close() }()
	paths := []tunPath{path4, path6}
	frame := make([]byte, 65536)
	for {
		count, _, receiveErr := unix.Recvfrom(3, frame, 0)
		if receiveErr != nil {
			return receiveErr
		}
		if count < tunFrameHeader {
			return errors.New("short TUN experiment frame")
		}
		operation := frame[0]
		if operation == 0xff {
			return nil
		}
		index := int(operation&0x0f) - 1
		if index < 0 || index >= len(paths) {
			return errors.New("invalid TUN family")
		}
		batch := int(frame[1])
		payloadLength := int(binary.BigEndian.Uint32(frame[2:6]))
		iterations := int(binary.BigEndian.Uint32(frame[6:10]))
		if batch < 1 || payloadLength < batch || payloadLength > count-tunFrameHeader || iterations < 1 || iterations > 1_000_000 {
			return errors.New("invalid TUN experiment limits")
		}
		packetBytes := payloadLength / batch
		udpBytes := packetBytes - paths[index].headerBytes
		if udpBytes < 1 || packetBytes*batch != payloadLength {
			return errors.New("invalid TUN packet size")
		}
		started := time.Now()
		if operation&0xf0 == 0x20 {
			if err = runSharedTun(paths[index], 3, frame, batch, packetBytes, udpBytes, iterations); err != nil {
				return err
			}
			continue
		}
		if operation&0xf0 == 0x30 {
			rights := unix.UnixRights(int(device.File().Fd()))
			if err = unix.Sendmsg(3, frame[:10], rights, nil, 0); err != nil {
				return err
			}
			for range iterations {
				for packet := range batch {
					start := tunFrameHeader + packet*packetBytes
					if err = paths[index].directRoundTrip(frame[start : start+udpBytes]); err != nil {
						return err
					}
				}
			}
			continue
		}
		for range iterations {
			for packet := range batch {
				start := tunFrameHeader + packet*packetBytes
				if err = paths[index].roundTrip(frame[start : start+udpBytes]); err != nil {
					return err
				}
			}
		}
		if operation&0xf0 == 0x10 {
			writeU64(frame[10:18], uint64(time.Since(started)))
			if err = unix.Send(3, frame[:18], 0); err != nil {
				return err
			}
		} else if err = unix.Send(3, frame[:count], 0); err != nil {
			return err
		}
	}
}

func makeTunPath(device gtun.Tun, network, local, remote string, headerBytes int) (tunPath, error) {
	localAddress := &net.UDPAddr{IP: net.ParseIP(local)}
	remoteAddress := &net.UDPAddr{IP: net.ParseIP(remote), Port: 42000}
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, err := net.DialUDP(network, localAddress, remoteAddress)
		if err == nil {
			return tunPath{device: device, connection: connection, headerBytes: headerBytes}, nil
		}
		if !errors.Is(err, unix.EADDRNOTAVAIL) || time.Now().After(deadline) {
			return tunPath{}, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func main() {
	err := run()
	_ = unix.Close(3)
	pid, parseErr := strconv.Atoi(os.Getenv("ALMAGEST_RESTRICTED_CHILD_PID"))
	if parseErr == nil {
		_, _ = unix.Wait4(pid, nil, 0, nil)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
