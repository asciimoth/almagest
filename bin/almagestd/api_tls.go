package main

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/pasta/pasta"
	"github.com/kataras/golog"
)

func (s *apiSession) handleTLSMessage(msg api.Message) (api.Message, error) {
	manager := s.admin.tls
	if manager == nil {
		return newRequestError(msg.ID, api.ErrorUnavailable, "tls storage is unavailable")
	}

	switch api.TLSMethod(msg.Method) {
	case api.MethodTLSSnapshot:
		snapshot, err := manager.tlsSnapshot()
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodTLSSubscribe:
		snapshot, err := manager.tlsSnapshot()
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		id := s.addTLSSubscription()
		return api.NewResponse(msg.ID, api.TLSSubscribeResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodTLSUnsubscribe:
		req, err := decodePayload[api.TLSSubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.removeTLSSubscription(req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodTLSBuiltinCA:
		snapshot, err := manager.tlsSnapshot()
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		if snapshot.BuiltinCA == nil {
			return tlsRequestError(msg.ID, errTLSCertNotFound)
		}
		return api.NewResponse(msg.ID, snapshot.BuiltinCA)

	case api.MethodTLSSetBuiltinCA:
		req, err := decodePayload[api.TLSSetBuiltinCARequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		info, result, err := s.admin.setTLSBuiltinCAOptions(req.Options)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		s.sendTLSMutationInbox(result)
		return api.NewResponse(msg.ID, info)

	case api.MethodTLSSetCert:
		req, err := decodePayload[api.TLSSetCertRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.setTLSCert(req.Name, req.PEM)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		s.sendTLSMutationInbox(result)
		entry, found, err := manager.tlsCertAPIEntry(req.Name)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		if !found {
			return tlsRequestError(msg.ID, errTLSCertNotFound)
		}
		return api.NewResponse(msg.ID, entry)

	case api.MethodTLSRenameCert:
		req, err := decodePayload[api.TLSRenameRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.renameTLSCert(req.Name, req.NewName)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		s.sendTLSMutationInbox(result)
		entry, found, err := manager.tlsCertAPIEntry(req.NewName)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		if !found {
			return tlsRequestError(msg.ID, errTLSCertNotFound)
		}
		return api.NewResponse(msg.ID, entry)

	case api.MethodTLSDeleteCert:
		req, err := decodePayload[api.TLSNameRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.deleteTLSCert(req.Name)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		s.sendTLSMutationInbox(result)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodTLSSetPolicy:
		req, err := decodePayload[api.TLSSetPolicyRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.setTLSPolicy(req.Name, req.Spec)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		s.sendTLSMutationInbox(result)
		entry, found, err := manager.tlsPolicyAPIEntry(req.Name)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		if !found {
			return tlsRequestError(msg.ID, errTLSPolicyNotFound)
		}
		return api.NewResponse(msg.ID, entry)

	case api.MethodTLSRenamePolicy:
		req, err := decodePayload[api.TLSRenameRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.renameTLSPolicy(req.Name, req.NewName)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		s.sendTLSMutationInbox(result)
		entry, found, err := manager.tlsPolicyAPIEntry(req.NewName)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		if !found {
			return tlsRequestError(msg.ID, errTLSPolicyNotFound)
		}
		return api.NewResponse(msg.ID, entry)

	case api.MethodTLSDeletePolicy:
		req, err := decodePayload[api.TLSNameRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		result, err := manager.deleteTLSPolicy(req.Name)
		if err != nil {
			return tlsRequestError(msg.ID, err)
		}
		s.sendTLSMutationInbox(result)
		return api.NewResponse(msg.ID, struct{}{})

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (a *AdminAPI) setTLSBuiltinCAOptions(options api.TLSBuiltinCAOptions) (api.TLSBuiltinCAInfo, tlsMutationResult, error) {
	if a == nil || a.tls == nil {
		return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, almagest.ErrTlsConfigNotFound
	}
	current := a.tls.currentTLSBuiltinCA()
	if current != nil && current.immutable {
		return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
	}
	if a.configReadOnly || a.config == nil {
		return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, ErrConfigRO
	}
	options, err := normalizeTLSBuiltinCAOptions(options)
	if err != nil {
		return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, err
	}
	golog.Debugf("builtin TLS CA: API requested options update to %s", describeTLSBuiltinCAOptions(options))

	var next *tlsBuiltinCA
	now := time.Now()
	needsRegeneration := current == nil ||
		!tlsBuiltinCAOptionsEqual(current.options, options) ||
		tlsBuiltinCACertExpired(current.certificate, now)
	if needsRegeneration {
		if a.fileStorage == "" {
			return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, almagest.ErrTlsConfigInvalidValue
		}
		golog.Debug("builtin TLS CA: API options update requires certificate regeneration")
		next, err = generateAndStoreTLSBuiltinCA(filepath.Join(a.fileStorage, tlsBuiltinCADirName), options, now)
		if err != nil {
			return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, err
		}
	} else {
		golog.Debug("builtin TLS CA: API options update does not require regeneration")
		next = current
	}

	if _, err := a.config.SetTLSBuiltinCAOptions(options); err != nil {
		return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, err
	}
	golog.Debug("builtin TLS CA: saved configured generation options")

	result := tlsMutationResult{}
	if needsRegeneration {
		result, err = a.tls.setTLSBuiltinCA(next)
		if err != nil {
			return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, err
		}
	}
	snapshot, err := a.tls.tlsSnapshot()
	if err != nil {
		return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, err
	}
	if snapshot.BuiltinCA == nil {
		return api.TLSBuiltinCAInfo{}, tlsMutationResult{}, errTLSCertNotFound
	}
	return *snapshot.BuiltinCA, result, nil
}

func (s *apiSession) sendTLSMutationInbox(result tlsMutationResult) {
	if len(result.policyNotifications) == 0 || s.engine == nil {
		return
	}
	for _, notification := range result.policyNotifications {
		payload := almagest.TlsConfigChanged{
			NamePrev: notification.prevName,
			NameNew:  notification.newName,
		}
		for _, node := range notification.nodes {
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
}

func (s *apiSession) addTLSSubscription() uint64 {
	s.tlsMu.Lock()
	defer s.tlsMu.Unlock()
	s.tlsNextSubscriptionID++
	id := s.tlsNextSubscriptionID
	if s.tlsSubscriptions == nil {
		s.tlsSubscriptions = map[uint64]struct{}{}
	}
	s.tlsSubscriptions[id] = struct{}{}
	if s.tlsChangeSubscriptionID == 0 {
		s.tlsChangeSubscriptionID = s.admin.tls.subscribeTLSChanges(func(change tlsChange) {
			s.sendTLSChange(change)
		})
	}
	return id
}

func (s *apiSession) removeTLSSubscription(id uint64) {
	s.tlsMu.Lock()
	defer s.tlsMu.Unlock()
	delete(s.tlsSubscriptions, id)
	if len(s.tlsSubscriptions) == 0 && s.tlsChangeSubscriptionID != 0 {
		s.admin.tls.unsubscribeTLSChanges(s.tlsChangeSubscriptionID)
		s.tlsChangeSubscriptionID = 0
	}
}

func (s *apiSession) closeTLSSubscriptions() {
	s.tlsMu.Lock()
	defer s.tlsMu.Unlock()
	if s.tlsChangeSubscriptionID != 0 && s.admin.tls != nil {
		s.admin.tls.unsubscribeTLSChanges(s.tlsChangeSubscriptionID)
	}
	s.tlsChangeSubscriptionID = 0
	s.tlsSubscriptions = nil
}

func (s *apiSession) sendTLSChange(change tlsChange) {
	ids := s.tlsSubscriptionSnapshot()
	for _, id := range ids {
		_ = s.sendTLSSubscriptionChange(id, change)
	}
}

func (s *apiSession) tlsSubscriptionSnapshot() []uint64 {
	s.tlsMu.Lock()
	defer s.tlsMu.Unlock()
	out := make([]uint64, 0, len(s.tlsSubscriptions))
	for id := range s.tlsSubscriptions {
		out = append(out, id)
	}
	return out
}

func (s *apiSession) sendTLSSubscriptionChange(id uint64, change tlsChange) error {
	var apiChange api.TLSChange
	switch change.kind {
	case tlsChangeBuiltinCA:
		snapshot, err := s.admin.tls.tlsSnapshot()
		if err != nil || snapshot.BuiltinCA == nil {
			return err
		}
		apiChange = api.TLSChange{
			Kind:      api.TLSChangeBuiltinCA,
			BuiltinCA: snapshot.BuiltinCA,
		}
	case tlsChangeCertUpsert:
		entry, found, err := s.admin.tls.tlsCertAPIEntry(change.name)
		if err != nil || !found {
			return err
		}
		apiChange = api.TLSChange{
			Kind: api.TLSChangeCert,
			Name: change.name,
			Cert: &entry,
		}
	case tlsChangeCertDelete:
		apiChange = api.TLSChange{
			Kind:    api.TLSChangeCert,
			Name:    change.name,
			Deleted: true,
		}
	case tlsChangePolicyUpsert:
		entry, found, err := s.admin.tls.tlsPolicyAPIEntry(change.name)
		if err != nil || !found {
			return err
		}
		apiChange = api.TLSChange{
			Kind:   api.TLSChangePolicy,
			Name:   change.name,
			Policy: &entry,
		}
	case tlsChangePolicyDelete:
		apiChange = api.TLSChange{
			Kind:    api.TLSChangePolicy,
			Name:    change.name,
			Deleted: true,
		}
	default:
		return nil
	}
	event, err := api.NewEvent(api.SurfaceTLS, string(api.EventTLSUpdate), api.TLSEvent{
		SubscriptionID: id,
		Change:         apiChange,
	})
	if err != nil {
		return err
	}
	return s.send(event)
}

func tlsRequestError(id string, err error) (api.Message, error) {
	switch {
	case errors.Is(err, almagest.ErrTlsConfigInvalidName), errors.Is(err, almagest.ErrTlsConfigInvalidValue):
		return newRequestError(id, api.ErrorBadRequest, err.Error())
	case errors.Is(err, errTLSCertNotFound), errors.Is(err, errTLSPolicyNotFound), errors.Is(err, almagest.ErrTlsConfigNotFound):
		return newRequestError(id, api.ErrorNotFound, err.Error())
	case errors.Is(err, almagest.ErrTlsConfigReadOnly), errors.Is(err, almagest.ErrTlsConfigConflict), errors.Is(err, ErrConfigRO):
		return newRequestError(id, api.ErrorConflict, err.Error())
	default:
		return newRequestError(id, api.ErrorInternal, err.Error())
	}
}
