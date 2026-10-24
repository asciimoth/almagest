// Package webdemo implements the in-process admin API used by the browser demo.
package webdemo

import (
	"fmt"
	"sync"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
)

// Backend serves the browser demo's live engine and storage APIs.
type Backend struct {
	mu                 sync.Mutex
	nextSubscriptionID uint64

	engine              almagest.Engine
	stores              *Storage
	config              *ConfigStore
	logs                *LogFactory
	engineSubscriptions map[uint64]uint64
	logSubscriptions    map[uint64]uint64
	subscriptionMu      sync.RWMutex
	dnsSubscriptions    map[uint64]dnsSubscription
	kvSubscriptions     map[uint64]kvSubscription
	secretSubscriptions map[uint64]struct{}
	eventMu             sync.Mutex
	events              []api.Message
}

// Handle accepts one JSON admin request and returns one JSON response.
func (b *Backend) Handle(data []byte) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	msg, err := api.UnmarshalMessage(data)
	if err != nil {
		return marshalError("0", api.ErrorInvalidPayload, err.Error())
	}
	if msg.Kind != api.MessageRequest {
		return marshalError(msg.ID, api.ErrorBadRequest, "web demo API accepts request messages")
	}
	response, err := b.liveRead(msg)
	if err != nil {
		return marshalError(msg.ID, api.ErrorInternal, err.Error())
	}
	encoded, err := api.MarshalMessage(response)
	if err != nil {
		return marshalError(msg.ID, api.ErrorInternal, err.Error())
	}
	return encoded
}

func (b *Backend) subscriptionID() uint64 {
	id := b.nextSubscriptionID
	b.nextSubscriptionID++
	return id
}

func marshalError(id string, code api.ErrorCode, message string) []byte {
	if id == "" {
		id = "0"
	}
	response, err := api.NewErrorResponse(id, code, message, nil)
	if err != nil {
		return []byte(fmt.Sprintf(`{"version":"v1","kind":"response","id":%q,"error":{"code":"internal","message":%q}}`, id, err.Error()))
	}
	encoded, err := api.MarshalMessage(response)
	if err != nil {
		return []byte(fmt.Sprintf(`{"version":"v1","kind":"response","id":%q,"error":{"code":"internal","message":%q}}`, id, err.Error()))
	}
	return encoded
}
