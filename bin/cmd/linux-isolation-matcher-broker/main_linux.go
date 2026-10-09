//go:build linux

// linux-isolation-matcher-broker owns the real checkpoint 12 sysnet matchers.
// It is not linked to almagestd.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/matcherexperiment"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"golang.org/x/sys/unix"
)

func tuple(proto string, local, remote net.Addr) (sockowner.FlowTuple, error) {
	localHost, localPort, err := net.SplitHostPort(local.String())
	if err != nil {
		return sockowner.FlowTuple{}, err
	}
	remoteHost, remotePort, err := net.SplitHostPort(remote.String())
	if err != nil {
		return sockowner.FlowTuple{}, err
	}
	lp, _ := strconv.ParseUint(localPort, 10, 16)
	rp, _ := strconv.ParseUint(remotePort, 10, 16)
	return sockowner.FlowTuple{Proto: proto, LocalIP: net.ParseIP(localHost), LocalPort: uint16(lp),
		RemoteIP: net.ParseIP(remoteHost), RemotePort: uint16(rp)}, nil
}

func tcpFlow(network, address string) (matcherexperiment.Flow, func(), error) {
	listener, err := net.Listen(network, address)
	if err != nil {
		return matcherexperiment.Flow{}, nil, err
	}
	client, err := net.Dial(network, listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return matcherexperiment.Flow{}, nil, err
	}
	server, err := listener.Accept()
	if err != nil {
		_ = client.Close()
		_ = listener.Close()
		return matcherexperiment.Flow{}, nil, err
	}
	value, err := tuple("tcp", client.LocalAddr(), client.RemoteAddr())
	closeAll := func() { _ = server.Close(); _ = client.Close(); _ = listener.Close() }
	return matcherexperiment.Flow{Name: network, Tuple: value}, closeAll, err
}

func udpFlow(network, address string) (matcherexperiment.Flow, func(), error) {
	server, err := net.ListenPacket(network, address)
	if err != nil {
		return matcherexperiment.Flow{}, nil, err
	}
	client, err := net.Dial(network, server.LocalAddr().String())
	if err != nil {
		_ = server.Close()
		return matcherexperiment.Flow{}, nil, err
	}
	value, err := tuple("udp", client.LocalAddr(), client.RemoteAddr())
	return matcherexperiment.Flow{Name: network, Tuple: value}, func() { _ = client.Close(); _ = server.Close() }, err
}

func send(value matcherexperiment.Response) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return unix.Send(3, data, 0)
}

func run() error {
	system, err := linuxsysnet.NewSystem(linuxsysnet.Config{Features: linuxsysnet.FeatureConfig{MatcherRules: true}})
	if err != nil {
		return err
	}
	defer func() { _ = system.Close() }()
	matcher, err := system.BuildMatcher(sysnet.Rule{Type: "uid", Rule: strconv.Itoa(os.Getuid())})
	if err != nil {
		return err
	}
	defer func() { _ = matcher.Close() }()
	builders := []func() (matcherexperiment.Flow, func(), error){
		func() (matcherexperiment.Flow, func(), error) { return tcpFlow("tcp4", "127.0.0.1:0") },
		func() (matcherexperiment.Flow, func(), error) { return tcpFlow("tcp6", "[::1]:0") },
		func() (matcherexperiment.Flow, func(), error) { return udpFlow("udp4", "127.0.0.1:0") },
		func() (matcherexperiment.Flow, func(), error) { return udpFlow("udp6", "[::1]:0") },
	}
	flows := make([]matcherexperiment.Flow, 0, len(builders))
	for _, builder := range builders {
		flow, closeFlow, buildErr := builder()
		if buildErr != nil {
			return buildErr
		}
		defer closeFlow()
		flows = append(flows, flow)
	}
	if err = send(matcherexperiment.Response{Flows: flows}); err != nil {
		return err
	}
	buffer := make([]byte, matcherexperiment.MaxMessageBytes)
	for {
		count, _, receiveErr := unix.Recvfrom(3, buffer, 0)
		if receiveErr != nil {
			return receiveErr
		}
		var request matcherexperiment.Request
		if decodeErr := json.Unmarshal(buffer[:count], &request); decodeErr != nil {
			if sendErr := send(matcherexperiment.Response{Error: "malformed_request"}); sendErr != nil {
				return sendErr
			}
			continue
		}
		switch request.Kind {
		case "close":
			return nil
		case "direct":
			if request.Iterations < 1 || request.Iterations > 1_000_000 {
				_ = send(matcherexperiment.Response{Error: "limit_exceeded"})
				continue
			}
			started := time.Now()
			matched := false
			for range request.Iterations {
				matched, err = matcher.Match(request.Flow)
				if err != nil {
					break
				}
			}
			response := matcherexperiment.Response{Matched: matched, DurationNS: time.Since(started).Nanoseconds()}
			if err != nil {
				response.Error = "backend_error"
			}
			if err = send(response); err != nil {
				return err
			}
		case "match":
			matched, matchErr := matcher.Match(request.Flow)
			response := matcherexperiment.Response{Matched: matched}
			if matchErr != nil {
				response.Error = "backend_error"
			}
			if err = send(response); err != nil {
				return err
			}
		default:
			if err = send(matcherexperiment.Response{Error: "unsupported_operation"}); err != nil {
				return err
			}
		}
	}
}

func main() {
	err := run()
	_ = unix.Close(3)
	pid, parseErr := strconv.Atoi(os.Getenv("ALMAGEST_RESTRICTED_CHILD_PID"))
	if parseErr == nil {
		_, _ = unix.Wait4(pid, nil, 0, nil)
	}
	if err != nil && !errors.Is(err, net.ErrClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
