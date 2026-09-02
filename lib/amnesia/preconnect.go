package amnesianodes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	wgoamnesia "github.com/asciimoth/wgo/amnesia"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const amnesiaPreConnectTimeout = 10 * time.Second

// executePreConnectActions executes server-declared preamble traffic through
// the node's graph-supplied network. It does not use the host network directly.
func executePreConnectActions(ctx context.Context, network nodekit.NetworkCloser, actions []wgoamnesia.PreConnectAction) error {
	return executePreConnectActionsLogged(ctx, network, actions, nil)
}

func executePreConnectActionsLogged(
	ctx context.Context,
	network nodekit.NetworkCloser,
	actions []wgoamnesia.PreConnectAction,
	logf func(string, ...any),
) error {
	if len(actions) == 0 {
		return nil
	}
	if !nodekit.NetworkIsReady(network) {
		amnesiaPreConnectLog(logf, "Amnesia pre-connect sequence blocked: actions=%d reason=api_network", len(actions))
		return errors.New("API network is required for Amnezia pre-connect actions")
	}
	amnesiaPreConnectLog(logf, "Amnesia pre-connect sequence starting: actions=%d", len(actions))
	for index, action := range actions {
		if err := executePreConnectActionLogged(ctx, network, action, index+1, logf); err != nil {
			amnesiaPreConnectLog(logf, "Amnesia pre-connect action failed: index=%d protocol=%q endpoint=%q", index+1, strings.ToLower(strings.TrimSpace(action.Protocol)), action.Endpoint)
			return fmt.Errorf("amnezia pre-connect action %d: %w", index+1, err)
		}
	}
	amnesiaPreConnectLog(logf, "Amnesia pre-connect sequence complete: actions=%d", len(actions))
	return nil
}

func executePreConnectActionLogged(
	ctx context.Context,
	network nodekit.NetworkCloser,
	action wgoamnesia.PreConnectAction,
	index int,
	logf func(string, ...any),
) error {
	timeout := action.Timeout
	if timeout <= 0 {
		timeout = amnesiaPreConnectTimeout
	}
	payload, err := wgoamnesia.BuildTaggedPayload(action.PayloadSpec, nil)
	if err != nil {
		return fmt.Errorf("build payload: %w", err)
	}
	expected, err := wgoamnesia.BuildTaggedPayload(action.ExpectedResponseSpec, nil)
	if err != nil {
		return fmt.Errorf("build expected response: %w", err)
	}
	protocol := strings.ToLower(strings.TrimSpace(action.Protocol))
	if protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("unsupported protocol %q", action.Protocol)
	}
	amnesiaPreConnectLog(
		logf,
		"Amnesia pre-connect action starting: index=%d protocol=%q endpoint=%q timeout=%s payload_bytes=%d expected_response_bytes=%d",
		index, protocol, action.Endpoint, timeout, len(payload), len(expected),
	)
	actionCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := network.Dial(actionCtx, protocol, action.Endpoint)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = connection.Close() }()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := actionCtx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}
	if _, err := connection.Write(payload); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if len(expected) == 0 {
		amnesiaPreConnectLog(logf, "Amnesia pre-connect action complete: index=%d protocol=%q endpoint=%q response_checked=false", index, protocol, action.Endpoint)
		return nil
	}
	reply := make([]byte, len(expected))
	if _, err := io.ReadFull(connection, reply); err != nil {
		return fmt.Errorf("read expected response: %w", err)
	}
	if !bytes.Equal(reply, expected) {
		return fmt.Errorf("expected response mismatch")
	}
	amnesiaPreConnectLog(logf, "Amnesia pre-connect action complete: index=%d protocol=%q endpoint=%q response_checked=true", index, protocol, action.Endpoint)
	return nil
}

func amnesiaPreConnectLog(logf func(string, ...any), format string, args ...any) {
	if logf != nil {
		logf(format, args...)
	}
}
