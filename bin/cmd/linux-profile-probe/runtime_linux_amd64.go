//go:build linux && amd64

package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	client "github.com/asciimoth/almagest/bin/internal/isolation/runtime"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
)

// This mode is compiled only into the Docker qualification executable. N sends
// a fixed-size bootstrap record over an inherited, credential-checked channel.
// No native backend or node callback is used to make runtime requests.
func runtimeClient() error {
	if err := linuxprofile.Seal(linuxprofile.Runtime); err != nil {
		return err
	}
	threadChecks(0, linuxprofile.Runtime)
	for name, passed := range output.Checks {
		if !passed {
			return fmt.Errorf("runtime boundary %s: %s", name, output.Errors[name])
		}
	}
	bind := func(fd uintptr) (transport.Stream, error) {
		return transport.BindSocket(os.NewFile(fd, "runtime-lane"), transport.LinuxPeer{UID: 0, GID: 0, OutsidePIDNamespace: true})
	}
	control, err := bind(3)
	if err != nil {
		return fmt.Errorf("bind control: %w", err)
	}
	defer control.Close() //nolint:errcheck
	data, err := bind(9)
	if err != nil {
		return fmt.Errorf("bind data: %w", err)
	}
	defer data.Close() //nolint:errcheck
	diagnostic, err := bind(10)
	if err != nil {
		return fmt.Errorf("bind diagnostics: %w", err)
	}
	defer diagnostic.Close() //nolint:errcheck
	var session protocol.Session
	var port uint16
	if err := control.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := binary.Read(control, binary.LittleEndian, &session); err != nil {
		return fmt.Errorf("bootstrap session: %w", err)
	}
	if err := binary.Read(control, binary.LittleEndian, &port); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.New(ctx, transport.Lanes{Control: control, Data: data, Diagnostic: diagnostic}, session)
	if err != nil {
		return fmt.Errorf("runtime transport: %w", err)
	}
	system := c.System()
	defer system.Close() //nolint:errcheck
	applicationDNS := newRuntimeDNS([4]byte{203, 0, 113, 77})
	defer applicationDNS.Close() //nolint:errcheck
	resolver := system.OutDNS()
	defer resolver.Close() //nolint:errcheck
	checkDNS := func() error {
		addresses, err := gdns.NewResolver(resolver).LookupIP(ctx, "ip4", "native.example.")
		if err != nil || len(addresses) != 1 || addresses[0].String() != "203.0.113.54" {
			return fmt.Errorf("native OutDNS answer: %v, %w", addresses, err)
		}
		return nil
	}
	if err := checkDNS(); err != nil {
		return err
	}
	network := system.OutNet()
	connection, err := network.DialUDP(ctx, "udp4", "", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("broker UDP: %w", err)
	}
	defer connection.Close() //nolint:errcheck
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err := connection.Write([]byte("restricted-runtime")); err != nil {
		return err
	}
	buffer := make([]byte, 128)
	n, err := connection.Read(buffer)
	if err != nil || string(buffer[:n]) != "restricted-runtime" {
		return fmt.Errorf("broker UDP echo: %q, %w", buffer[:n], err)
	}
	device, err := system.BuildTun(sysnet.TunOpts{Name: "composed-tun", MTU: 1400, TunAddrs: []string{"198.18.4.1/30"}})
	if err != nil {
		return fmt.Errorf("broker TUN: %w", err)
	}
	if device.File() != nil || device.IsNative() {
		return errors.New("runtime obtained native TUN access")
	}
	if err := device.Close(); err != nil {
		return fmt.Errorf("broker TUN close: %w", err)
	}
	listener, err := system.LocalNet().ListenTCP(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("broker TCP listen: %w", err)
	}
	defer listener.Close() //nolint:errcheck
	tcp, err := network.DialTCP(ctx, "tcp4", "", listener.Addr().String())
	if err != nil {
		return fmt.Errorf("broker TCP dial: %w", err)
	}
	defer tcp.Close() //nolint:errcheck
	accepted, err := listener.Accept()
	if err != nil {
		return fmt.Errorf("broker TCP accept: %w", err)
	}
	defer accepted.Close() //nolint:errcheck
	matcher, err := system.BuildMatcher(sysnet.Rule{Type: "uid", Rule: "0"})
	if err != nil {
		return fmt.Errorf("broker owner matcher: %w", err)
	}
	defer matcher.Close() //nolint:errcheck
	if matches, err := sysnet.MatchConn(accepted, matcher); err != nil || !matches {
		return fmt.Errorf("broker owner match: %v, %w", matches, err)
	}
	if err := tcp.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if err := accepted.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err := tcp.Write([]byte("tcp")); err != nil {
		return err
	}
	n, err = accepted.Read(buffer)
	if err != nil || string(buffer[:n]) != "tcp" {
		return fmt.Errorf("broker TCP data: %q, %w", buffer[:n], err)
	}
	def, err := system.BuildDefaultTun(sysnet.DefaultTunOpts{DnsIP: "198.18.5.1", MTU: 1400, TunAddrs: []string{"198.18.5.1/32"}})
	if err != nil {
		return fmt.Errorf("broker default TUN: %w", err)
	}
	if def.File() != nil || def.IsNative() {
		return errors.New("runtime obtained native default TUN access")
	}
	stableDef := def
	if err = def.SetDNS(applicationDNS); err != nil {
		return fmt.Errorf("attach application DNS: %w", err)
	}
	if def != stableDef || def.File() != nil || def.IsNative() {
		return errors.New("DNS mutation changed the stable restricted TUN adapter")
	}
	managedDNS := gdns.NewClient(network.Dial, nil, nil, "udp://198.18.5.1:53")
	addresses, err := gdns.NewResolver(managedDNS).LookupIP(ctx, "ip4", "application.example.")
	_ = managedDNS.Close()
	if err != nil || len(addresses) != 1 || addresses[0].String() != "203.0.113.77" {
		return fmt.Errorf("application DNS answer: %v, %w", addresses, err)
	}
	if err := checkDNS(); err != nil {
		return err
	}
	// Deliberately leave the owned default TUN live. Closing the complete system
	// must make N remove it and restore DNS.
	// A broker channel does not permit direct native sockets after useful I/O.
	if socket, err := net.ListenPacket("udp4", "127.0.0.1:0"); err == nil {
		_ = socket.Close()
		return errors.New("runtime gained native network access")
	}
	fmt.Println("restricted runtime: authenticated lanes, native OutDNS, application DNS, TCP/UDP, owner matching, regular/default TUN and native access denial passed")
	return nil
}

type runtimeDNS struct {
	requests chan gdns.Request
	stop     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
	answer   [4]byte
}

func newRuntimeDNS(answer [4]byte) *runtimeDNS {
	d := &runtimeDNS{requests: make(chan gdns.Request), stop: make(chan struct{}), answer: answer}
	d.wg.Go(func() {
		for {
			select {
			case <-d.stop:
				return
			case request := <-d.requests:
				message := &gdns.Message{ID: request.Message.ID, Response: true, RecursionAvailable: true, Questions: request.Message.Questions}
				for _, question := range message.Questions {
					if question.Type == gdns.TypeA {
						message.Answers = append(message.Answers, gdns.Resource{Name: question.Name, Type: gdns.TypeA, Class: gdns.ClassIN, TTL: 60, Data: append([]byte(nil), d.answer[:]...)})
					}
				}
				select {
				case request.Reply <- gdns.Response{Message: message}:
				case <-request.Context.Done():
				case <-d.stop:
					return
				}
			}
		}
	})
	return d
}

func (d *runtimeDNS) Requests() chan<- gdns.Request { return d.requests }
func (d *runtimeDNS) Close() error {
	d.once.Do(func() { close(d.stop) })
	d.wg.Wait()
	return nil
}
