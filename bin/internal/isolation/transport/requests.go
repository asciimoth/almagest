package transport

import (
	"encoding/json"
	"reflect"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// The dispatch table is closed. Adding a native operation requires a reviewed
// protocol type; no arbitrary method, syscall or native payload is forwarded.
func requestType(op protocol.Operation) (any, bool) {
	switch op {
	case protocol.OperationCapabilities, protocol.OperationStatus, protocol.OperationListInterfaces, protocol.OperationResolverSnapshot:
		return new(protocol.ReadRequest), false
	case protocol.OperationCheckTun:
		return new(protocol.CheckTunRequest), false
	case protocol.OperationCheckDefaultTun:
		return new(protocol.CheckDefaultTunRequest), false
	case protocol.OperationCheckRule:
		return new(protocol.CheckRuleRequest), false
	case protocol.OperationCompleteRule:
		return new(protocol.CompleteRuleRequest), false
	case protocol.OperationReserveIP, protocol.OperationReserveSubnet:
		return new(protocol.ReserveAddressRequest), true
	case protocol.OperationCreateTun, protocol.OperationCreateDefaultTun:
		return new(protocol.CreateTunRequest), true
	case protocol.OperationSetTunMTU, protocol.OperationSetTunAddresses, protocol.OperationSetTunRoutes, protocol.OperationSetTunName:
		return new(protocol.UpdateTunRequest), true
	case protocol.OperationConfigureDNS:
		return new(protocol.ConfigureDNSRequest), true
	case protocol.OperationOpenSocket:
		return new(protocol.OpenSocketRequest), true
	case protocol.OperationCreateMatcher:
		return new(protocol.CreateMatcherRequest), true
	case protocol.OperationMatch:
		return new(protocol.MatchRequest), false
	case protocol.OperationGetTunState:
		return new(protocol.ResourceRequest), false
	case protocol.OperationReleaseAllocation, protocol.OperationCloseResource:
		return new(protocol.ResourceRequest), true
	case protocol.OperationReleaseGraphEpoch:
		return new(protocol.ReleaseGraphEpochRequest), true
	case protocol.OperationReadConfig:
		return new(protocol.ReadConfigRequest), false
	case protocol.OperationReplaceConfig:
		return new(protocol.ReplaceConfigRequest), true
	case protocol.OperationGetOperationResult:
		return new(protocol.OperationResultRequest), false
	case protocol.OperationRunNodeCommand:
		return new(protocol.NodeCommandRequest), true
	case protocol.OperationRunCallback:
		return new(protocol.CallbackRequest), true
	default:
		return nil, false
	}
}

func validateRequest(op protocol.Operation, body []byte, id uint64, session protocol.Session, outgoing bool) ([]byte, error) {
	request, mutation := requestType(op)
	if request == nil {
		return nil, protocol.ErrUnsupportedOperation
	}
	if err := Decode(body, request); err != nil {
		return nil, err
	}
	field := reflect.ValueOf(request).Elem().FieldByName("Meta")
	meta := field.Interface().(protocol.RequestMeta)
	if outgoing {
		meta.RequestID, meta.SessionID, meta.SessionEpoch = id, session.ID, session.Epoch
	}
	if meta.RequestID != id {
		return nil, protocol.ErrInvalidContract
	}
	if err := meta.Validate(session, mutation); err != nil {
		return nil, err
	}
	field.Set(reflect.ValueOf(meta))
	return json.Marshal(request)
}
