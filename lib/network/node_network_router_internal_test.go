package networknodes

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/asciimoth/gonnect"
)

func TestNetworkRouterTreatsClosedAttachNotificationsAsNonFatal(t *testing.T) {
	router := gonnect.NewRouter(nil)
	t.Cleanup(func() { _ = router.Close() })
	backend := gonnect.NewLoopbackNetwork()
	if err := router.Attach(1, backend); err != nil {
		t.Fatalf("Attach backend: %v", err)
	}
	node := &networkRouterNode{router: router}

	closedNotifications := errors.Join(net.ErrClosed, fmt.Errorf("notify downstream: %w", net.ErrClosed))
	if !node.isNonFatalAttachError(closedNotifications) {
		t.Fatalf("closed notification error classified as fatal: %v", closedNotifications)
	}
	mixedErr := errors.Join(net.ErrClosed, errors.New("subscribe failed"))
	if node.isNonFatalAttachError(mixedErr) {
		t.Fatalf("mixed attach error classified as non-fatal: %v", mixedErr)
	}
	_ = router.Close()
	if node.isNonFatalAttachError(net.ErrClosed) {
		t.Fatal("closed router attach error classified as non-fatal")
	}
}
