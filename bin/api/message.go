package api

import (
	"encoding/json"
	"errors"
	"fmt"
)

const Version = "v1"

var (
	ErrEmptyMessageID = errors.New("api message id is empty")
	ErrEmptySurface   = errors.New("api message surface is empty")
	ErrEmptyMethod    = errors.New("api message method is empty")
	ErrNilResponse    = errors.New("api response must include result or error")
)

type MessageKind string

const (
	MessageRequest  MessageKind = "request"
	MessageResponse MessageKind = "response"
	MessageEvent    MessageKind = "event"
)

type Surface string

const (
	SurfaceAdmin   Surface = "admin"
	SurfaceDNS     Surface = "dns"
	SurfaceEngine  Surface = "engine"
	SurfaceKV      Surface = "kv"
	SurfaceLog     Surface = "log"
	SurfaceSecrets Surface = "secrets"
	SurfaceSystem  Surface = "system"
	SurfaceTLS     Surface = "tls"
)

func Surfaces() []string {
	return []string{
		string(SurfaceAdmin),
		string(SurfaceDNS),
		string(SurfaceEngine),
		string(SurfaceKV),
		string(SurfaceLog),
		string(SurfaceSecrets),
		string(SurfaceSystem),
		string(SurfaceTLS),
	}
}

func RequiresMutationLease(surface Surface, method string) bool {
	switch surface {
	case SurfaceEngine:
		return IsEngineMutation(method)
	case SurfaceSecrets:
		return IsSecretsMutation(method)
	case SurfaceTLS:
		return IsTLSMutation(method)
	default:
		return false
	}
}

// Message is the shared JSON envelope.
//
// ID is required for requests and responses. Clients and servers each own the
// IDs they create; the other side must echo request IDs in matching responses.
// Events are uncorrelated and may omit ID.
type Message struct {
	Version string          `json:"version"`
	Kind    MessageKind     `json:"kind"`
	ID      string          `json:"id,omitempty"`
	Surface Surface         `json:"surface,omitempty"`
	Method  string          `json:"method,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    ErrorCode       `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

type ErrorCode string

const (
	ErrorBadRequest       ErrorCode = "bad_request"
	ErrorUnauthorized     ErrorCode = "unauthorized"
	ErrorForbidden        ErrorCode = "forbidden"
	ErrorNotFound         ErrorCode = "not_found"
	ErrorConflict         ErrorCode = "conflict"
	ErrorInvalidMethod    ErrorCode = "invalid_method"
	ErrorInvalidPayload   ErrorCode = "invalid_payload"
	ErrorUnavailable      ErrorCode = "unavailable"
	ErrorInternal         ErrorCode = "internal"
	ErrorMutationRejected ErrorCode = "mutation_rejected"
)

func NewRequest(id string, surface Surface, method string, payload any) (Message, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	msg := Message{
		Version: Version,
		Kind:    MessageRequest,
		ID:      id,
		Surface: surface,
		Method:  method,
		Payload: raw,
	}
	return msg, msg.Validate()
}

func NewResponse(id string, result any) (Message, error) {
	raw, err := marshalPayload(result)
	if err != nil {
		return Message{}, err
	}
	msg := Message{
		Version: Version,
		Kind:    MessageResponse,
		ID:      id,
		Result:  raw,
	}
	return msg, msg.Validate()
}

func NewErrorResponse(id string, code ErrorCode, message string, details any) (Message, error) {
	raw, err := marshalPayload(details)
	if err != nil {
		return Message{}, err
	}
	msg := Message{
		Version: Version,
		Kind:    MessageResponse,
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
			Details: raw,
		},
	}
	return msg, msg.Validate()
}

func NewEvent(surface Surface, method string, payload any) (Message, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	msg := Message{
		Version: Version,
		Kind:    MessageEvent,
		Surface: surface,
		Method:  method,
		Payload: raw,
	}
	return msg, msg.Validate()
}

func (m Message) Validate() error {
	if m.Version == "" {
		return fmt.Errorf("api message version is empty")
	}
	switch m.Kind {
	case MessageRequest:
		if m.ID == "" {
			return ErrEmptyMessageID
		}
		if m.Surface == "" {
			return ErrEmptySurface
		}
		if m.Method == "" {
			return ErrEmptyMethod
		}
	case MessageResponse:
		if m.ID == "" {
			return ErrEmptyMessageID
		}
		if len(m.Result) == 0 && m.Error == nil {
			return ErrNilResponse
		}
	case MessageEvent:
		if m.Surface == "" {
			return ErrEmptySurface
		}
		if m.Method == "" {
			return ErrEmptyMethod
		}
	default:
		return fmt.Errorf("unknown api message kind %q", m.Kind)
	}
	return nil
}

func DecodePayload[T any](raw json.RawMessage) (T, error) {
	var value T
	if len(raw) == 0 {
		return value, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, err
	}
	return value, nil
}

func MarshalMessage(msg Message) ([]byte, error) {
	if err := msg.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(msg)
}

func UnmarshalMessage(data []byte) (Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return Message{}, err
	}
	return msg, msg.Validate()
}

func marshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}
