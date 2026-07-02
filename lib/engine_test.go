package almagest

import (
	"context"
	"testing"

	"github.com/asciimoth/configer/configer"
)

func TestEngineUnsubscribeNotificationsRemovesSubscriptionMapping(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1),
		newE2EDebugSystem(),
		nil,
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	engineImpl := engineAPI.(*engine)
	for range 10 {
		id := engineAPI.SubscribeNotifications(func(EngineNotification) {})
		if id == 0 {
			t.Fatal("SubscribeNotifications returned 0")
		}
		if got := len(engineImpl.subscriptionIDMapping); got != 1 {
			t.Fatalf("subscription mapping count after subscribe = %d, want 1", got)
		}

		engineAPI.UnsubscribeNotifications(id)
		if got := len(engineImpl.subscriptionIDMapping); got != 0 {
			t.Fatalf("subscription mapping count after unsubscribe = %d, want 0", got)
		}
	}
}
