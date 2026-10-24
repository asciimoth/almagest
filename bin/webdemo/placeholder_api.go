package webdemo

import "github.com/asciimoth/almagest/bin/api"

type emptyTLSSnapshot struct {
	Certs    []api.TLSCertEntry   `json:"certs"`
	Policies []api.TLSPolicyEntry `json:"policies"`
}

func (b *Backend) handleTLS(msg api.Message) (api.Message, error) {
	if api.IsTLSMutation(msg.Method) {
		return requestError(msg.ID, api.ErrorForbidden, "TLS mutations are not available in the web demo")
	}

	empty := emptyTLSSnapshot{
		Certs:    make([]api.TLSCertEntry, 0),
		Policies: make([]api.TLSPolicyEntry, 0),
	}
	switch api.TLSMethod(msg.Method) {
	case api.MethodTLSSnapshot:
		if _, err := payload[struct{}](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		return api.NewResponse(msg.ID, empty)
	case api.MethodTLSSubscribe:
		if _, err := payload[struct{}](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct {
			SubscriptionID uint64           `json:"subscription_id"`
			Snapshot       emptyTLSSnapshot `json:"snapshot"`
		}{
			SubscriptionID: b.subscriptionID(),
			Snapshot:       empty,
		})
	case api.MethodTLSUnsubscribe:
		if _, err := payload[api.TLSSubscriptionRequest](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodTLSBuiltinCA, api.MethodTLSBuiltinCRL:
		if _, err := payload[struct{}](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		return requestError(msg.ID, api.ErrorNotFound, "the web demo has no local certificate authority")
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) handleSnapshots(msg api.Message) (api.Message, error) {
	if api.IsSnapshotsMutation(msg.Method) {
		return requestError(msg.ID, api.ErrorForbidden, "snapshot mutations are not available in the web demo")
	}

	switch api.SnapshotsMethod(msg.Method) {
	case api.MethodSnapshotsList:
		if _, err := payload[struct{}](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		return api.NewResponse(msg.ID, api.SnapshotsListResponse{Snapshots: make([]api.SnapshotInfo, 0)})
	case api.MethodSnapshotsExport:
		if _, err := payload[api.SnapshotUUIDRequest](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		return requestError(msg.ID, api.ErrorNotFound, "the web demo has no snapshots")
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}
