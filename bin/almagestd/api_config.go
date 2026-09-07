package main

import (
	"errors"

	"github.com/asciimoth/almagest/bin/api"
)

func (s *apiSession) handleConfigMessage(msg api.Message) (api.Message, error) {
	if s.admin.configAutosaver == nil {
		return newRequestError(msg.ID, api.ErrorUnavailable, "config service is not ready")
	}

	switch api.ConfigMethod(msg.Method) {
	case api.MethodConfigSnapshot:
		if _, err := decodePayload[struct{}](msg); err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, err := s.admin.configAutosaver.Snapshot(s.admin.configReadOnly)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInternal, err.Error())
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodConfigUpdate:
		if s.admin.configReadOnly {
			return newRequestError(msg.ID, api.ErrorForbidden, "config is read-only")
		}
		req, err := decodePayload[api.ConfigUpdateRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		response, err := s.admin.configAutosaver.Update(req.Text, req.Revision)
		if err == nil {
			return api.NewResponse(msg.ID, response)
		}
		switch {
		case errors.Is(err, errConfigRevisionConflict):
			return newRequestError(msg.ID, api.ErrorConflict, err.Error())
		case errors.Is(err, errConfigInvalid), errors.Is(err, errConfigProtectedContent):
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		default:
			return newRequestError(msg.ID, api.ErrorMutationRejected, err.Error())
		}

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}
