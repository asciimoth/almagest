package main

import (
	"errors"
	"fmt"
	"github.com/asciimoth/almagest/lib/storage"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/pasta/pasta"
)

var (
	errSecretEngineUnavailable = errors.New("engine is unavailable for secret node validation")
	errSecretUnknownNode       = errors.New("secret allows unknown node")
)

func (s *apiSession) handleSecretsMessage(msg api.Message) (api.Message, error) {
	manager := s.admin.secrets
	if manager == nil {
		return newRequestError(msg.ID, api.ErrorUnavailable, "secrets storage is unavailable")
	}

	switch api.SecretsMethod(msg.Method) {
	case api.MethodSecretsSnapshot:
		snapshot, err := manager.secretsSnapshot()
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodSecretsSubscribe:
		snapshot, err := manager.secretsSnapshot()
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		id := s.addSecretsSubscription()
		return api.NewResponse(msg.ID, api.SecretsSubscribeResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodSecretsUnsubscribe:
		req, err := decodePayload[api.SecretsSubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.removeSecretsSubscription(req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodSecretsSetValue:
		if s.engine == nil {
			return secretRequestError(msg.ID, errSecretEngineUnavailable)
		}
		req, err := decodePayload[api.SecretsSetValueRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.setSecretValue(req.Key, req.Value)
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		s.sendSecretMutationInbox(result)
		entry, found, err := manager.secretAPIEntry(req.Key)
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		if !found {
			return secretRequestError(msg.ID, errSecretNotFound)
		}
		return api.NewResponse(msg.ID, entry)

	case api.MethodSecretsSetNodes:
		if s.engine == nil {
			return secretRequestError(msg.ID, errSecretEngineUnavailable)
		}
		req, err := decodePayload[api.SecretsSetNodesRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := s.validateSecretNodes(req.Nodes); err != nil {
			return secretRequestError(msg.ID, err)
		}
		result, err := manager.setSecretNodes(req.Key, req.Nodes)
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		s.sendSecretMutationInbox(result)
		entry, found, err := manager.secretAPIEntry(req.Key)
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		if !found {
			return secretRequestError(msg.ID, errSecretNotFound)
		}
		return api.NewResponse(msg.ID, entry)

	case api.MethodSecretsRename:
		if s.engine == nil {
			return secretRequestError(msg.ID, errSecretEngineUnavailable)
		}
		req, err := decodePayload[api.SecretsRenameRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.renameSecret(req.Key, req.NewKey)
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		s.sendSecretMutationInbox(result)
		entry, found, err := manager.secretAPIEntry(req.NewKey)
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		if !found {
			return secretRequestError(msg.ID, errSecretNotFound)
		}
		return api.NewResponse(msg.ID, entry)

	case api.MethodSecretsDelete:
		if s.engine == nil {
			return secretRequestError(msg.ID, errSecretEngineUnavailable)
		}
		req, err := decodePayload[api.SecretKeyRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.deleteSecret(req.Key)
		if err != nil {
			return secretRequestError(msg.ID, err)
		}
		s.sendSecretMutationInbox(result)
		return api.NewResponse(msg.ID, struct{}{})

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (s *apiSession) validateSecretNodes(nodes []string) error {
	if !validSecretNodeNames(nodes) {
		return storage.ErrSecretInvalidName
	}
	for _, node := range normalizeSecretNodes(nodes) {
		if _, ok := s.engine.NodeByName("main", node); !ok {
			return fmt.Errorf("%w: %s", errSecretUnknownNode, node)
		}
	}
	return nil
}

func (s *apiSession) sendSecretMutationInbox(result secretMutationResult) {
	if len(result.notifyNodes) == 0 || s.engine == nil {
		return
	}
	payload := storage.SecretValueChanged{
		SecretNamePrev: result.prevKey,
		SecretNameNew:  result.newKey,
	}
	for _, node := range normalizeSecretNodes(result.notifyNodes) {
		id, ok := s.engine.NodeByName("main", node)
		if !ok {
			continue
		}
		s.engine.SendInbox("main", pasta.InboxMessage{
			ReceiverNode: id,
			Payload:      payload,
		})
	}
}

func (s *apiSession) addSecretsSubscription() uint64 {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	s.secretsNextSubscriptionID++
	id := s.secretsNextSubscriptionID
	if s.secretsSubscriptions == nil {
		s.secretsSubscriptions = map[uint64]struct{}{}
	}
	s.secretsSubscriptions[id] = struct{}{}
	if s.secretsChangeSubscriptionID == 0 {
		s.secretsChangeSubscriptionID = s.admin.secrets.subscribeSecretChanges(func(change secretChange) {
			s.sendSecretChange(change)
		})
	}
	return id
}

func (s *apiSession) removeSecretsSubscription(id uint64) {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	delete(s.secretsSubscriptions, id)
	if len(s.secretsSubscriptions) == 0 && s.secretsChangeSubscriptionID != 0 {
		s.admin.secrets.unsubscribeSecretChanges(s.secretsChangeSubscriptionID)
		s.secretsChangeSubscriptionID = 0
	}
}

func (s *apiSession) closeSecretsSubscriptions() {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	if s.secretsChangeSubscriptionID != 0 && s.admin.secrets != nil {
		s.admin.secrets.unsubscribeSecretChanges(s.secretsChangeSubscriptionID)
	}
	s.secretsChangeSubscriptionID = 0
	s.secretsSubscriptions = nil
}

func (s *apiSession) sendSecretChange(change secretChange) {
	ids := s.secretsSubscriptionSnapshot()
	for _, id := range ids {
		_ = s.sendSecretSubscriptionChange(id, change)
	}
}

func (s *apiSession) secretsSubscriptionSnapshot() []uint64 {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	out := make([]uint64, 0, len(s.secretsSubscriptions))
	for id := range s.secretsSubscriptions {
		out = append(out, id)
	}
	return out
}

func (s *apiSession) sendSecretSubscriptionChange(id uint64, change secretChange) error {
	var apiChange api.SecretChange
	switch change.kind {
	case secretChangeUpsert:
		entry, found, err := s.admin.secrets.secretAPIEntry(change.key)
		if err != nil || !found {
			return err
		}
		apiChange = api.SecretChange{
			Key:   change.key,
			Entry: &entry,
		}
	case secretChangeDelete:
		apiChange = api.SecretChange{
			Key:     change.key,
			Deleted: true,
		}
	default:
		return nil
	}
	event, err := api.NewEvent(api.SurfaceSecrets, string(api.EventSecretsUpdate), api.SecretsEvent{
		SubscriptionID: id,
		Change:         apiChange,
	})
	if err != nil {
		return err
	}
	return s.send(event)
}

func secretRequestError(id string, err error) (api.Message, error) {
	switch {
	case errors.Is(err, storage.ErrSecretClosed):
		return newRequestError(id, api.ErrorUnavailable, err.Error())
	case errors.Is(err, storage.ErrSecretInvalidName), errors.Is(err, storage.ErrSecretInvalidValue):
		return newRequestError(id, api.ErrorBadRequest, err.Error())
	case errors.Is(err, errSecretUnknownNode):
		return newRequestError(id, api.ErrorNotFound, err.Error())
	case errors.Is(err, errSecretNotFound):
		return newRequestError(id, api.ErrorNotFound, err.Error())
	case errors.Is(err, storage.ErrSecretReadOnly), errors.Is(err, storage.ErrSecretConflict):
		return newRequestError(id, api.ErrorConflict, err.Error())
	case errors.Is(err, errSecretEngineUnavailable):
		return newRequestError(id, api.ErrorUnavailable, err.Error())
	default:
		return newRequestError(id, api.ErrorInternal, err.Error())
	}
}
