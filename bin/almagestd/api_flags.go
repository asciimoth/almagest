package main

import (
	"context"

	"github.com/asciimoth/almagest/bin/api"
)

func (s *apiSession) handleFlagsMessage(msg api.Message) (api.Message, error) {
	if s.admin.kv == nil || s.admin.kv.flagsPath == "" {
		return newRequestError(msg.ID, api.ErrorUnavailable, "flags storage is unavailable")
	}
	switch api.FlagsMethod(msg.Method) {
	case api.MethodFlagsSnapshot:
		if _, err := decodePayload[struct{}](msg); err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		return api.NewResponse(msg.ID, s.admin.kv.flagsSnapshot())
	case api.MethodFlagsReplace:
		if len(msg.Payload) > flagsMaxBytes {
			return newRequestError(msg.ID, api.ErrorTooLarge, "flags document exceeds the size limit")
		}
		document, err := decodeFlagsDocument(msg.Payload)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := s.admin.kv.replaceFlags(context.Background(), document); err != nil {
			return newRequestError(msg.ID, api.ErrorInternal, err.Error())
		}
		return api.NewResponse(msg.ID, s.admin.kv.flagsSnapshot())
	case api.MethodFlagsMarkOpened:
		payload, err := decodePayload[api.FlagsMarkOpenedRequest](msg)
		if err != nil || (payload.Page != "docs" && payload.Page != "snapshots") {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, "page must be docs or snapshots")
		}
		document, err := s.admin.kv.markOpened(context.Background(), payload.Page)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInternal, err.Error())
		}
		return api.NewResponse(msg.ID, document)
	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (s *apiSession) sendFlagsSnapshot() error {
	if s.admin.kv == nil || s.admin.kv.flagsPath == "" {
		return nil
	}
	release, err := s.admin.mutations.Acquire(false, false)
	if err != nil {
		return err
	}
	document := s.admin.kv.flagsSnapshot()
	release()
	event, err := api.NewEvent(api.SurfaceFlags, string(api.EventFlagsSnapshot), document)
	if err != nil {
		return err
	}
	return s.send(event)
}
