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
	case protocol.OperationCapabilities, protocol.OperationStatus, protocol.OperationResolverSnapshot, protocol.OperationRuntimeReady:
		return new(protocol.ReadRequest), false
	case protocol.OperationTLSPlatformRefresh:
		return new(protocol.TLSPlatformRefreshRequest), false
	case protocol.OperationListInterfaces:
		return new(protocol.InterfaceRequest), false
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
	case protocol.OperationCreateDNSEndpoint:
		return new(protocol.CreateDNSEndpointRequest), true
	case protocol.OperationChannel:
		return new(protocol.ChannelRequest), false
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

// prepareRequest copies an exact closed-protocol request before trusted
// transport metadata is added. The copy prevents Call from changing the
// caller's metadata. Incoming JSON still passes through Decode; this path
// accepts no maps, aliases, or other open shapes.
func prepareRequest(op protocol.Operation, value any) (any, bool, error) {
	request, mutation := requestType(op)
	if request == nil {
		return nil, false, protocol.ErrUnsupportedOperation
	}
	want := reflect.TypeOf(request)
	got := reflect.ValueOf(value)
	if !got.IsValid() {
		return nil, false, protocol.ErrInvalidContract
	}
	destination := reflect.ValueOf(request).Elem()
	switch got.Type() {
	case want:
		if got.IsNil() {
			return nil, false, protocol.ErrInvalidContract
		}
		destination.Set(got.Elem())
	case want.Elem():
		destination.Set(got)
	default:
		return nil, false, protocol.ErrInvalidContract
	}
	return request, mutation, nil
}

func marshalPreparedRequest(request any, mutation bool, id uint64, session protocol.Session) ([]byte, error) {
	field := reflect.ValueOf(request).Elem().FieldByName("Meta")
	meta := field.Interface().(protocol.RequestMeta)
	meta.RequestID, meta.SessionID, meta.SessionEpoch = id, session.ID, session.Epoch
	if err := meta.Validate(session, mutation); err != nil {
		return nil, err
	}
	field.Set(reflect.ValueOf(meta))
	body, err := json.Marshal(request)
	if err != nil {
		return nil, protocol.ErrInvalidContract
	}
	return body, nil
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
