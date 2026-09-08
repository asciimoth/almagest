package main

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/asciimoth/almagest/bin/api"
)

func (s *apiSession) handleSnapshotsMessage(msg api.Message) (api.Message, error) {
	service := s.admin.snapshots
	if service == nil {
		return newRequestError(msg.ID, api.ErrorUnavailable, "snapshot service is not ready")
	}

	switch api.SnapshotsMethod(msg.Method) {
	case api.MethodSnapshotsCreate:
		req, err := decodePayload[api.SnapshotsCreateRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		info, err := service.Create(context.Background(), req.Name)
		if err != nil {
			return snapshotRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, info)

	case api.MethodSnapshotsList:
		if _, err := decodePayload[struct{}](msg); err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshots, err := service.List()
		if err != nil {
			return snapshotRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, api.SnapshotsListResponse{Snapshots: snapshots})

	case api.MethodSnapshotsExport:
		req, err := decodePayload[api.SnapshotUUIDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if strings.TrimSpace(req.UUID) == "" {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, "snapshot UUID is required")
		}
		response, err := service.Export(req.UUID)
		if err != nil {
			return snapshotRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, response)

	case api.MethodSnapshotsImport:
		req, err := decodePayload[api.SnapshotsImportRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if len(req.Archive) == 0 {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, "snapshot archive is required")
		}
		if int64(len(req.Archive)) > api.SnapshotArchiveMaxBytes {
			return newRequestError(msg.ID, api.ErrorTooLarge, "snapshot archive exceeds the 32 MiB limit")
		}
		info, err := service.Import(context.Background(), bytes.NewReader(req.Archive), req.Name)
		if err != nil {
			return snapshotRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, info)

	case api.MethodSnapshotsDelete:
		req, err := decodePayload[api.SnapshotUUIDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if strings.TrimSpace(req.UUID) == "" {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, "snapshot UUID is required")
		}
		if err := service.Delete(req.UUID); err != nil {
			return snapshotRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodSnapshotsRename:
		req, err := decodePayload[api.SnapshotsRenameRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if strings.TrimSpace(req.UUID) == "" {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, "snapshot UUID is required")
		}
		info, err := service.Rename(req.UUID, req.Name)
		if err != nil {
			return snapshotRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, info)

	case api.MethodSnapshotsRestore:
		req, err := decodePayload[api.SnapshotUUIDRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if strings.TrimSpace(req.UUID) == "" {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, "snapshot UUID is required")
		}
		response, err := service.Restore(context.Background(), req.UUID)
		if err != nil {
			return snapshotRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, response)

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func snapshotRequestError(id string, err error) (api.Message, error) {
	switch {
	case errors.Is(err, errSnapshotNotFound):
		return newRequestError(id, api.ErrorNotFound, err.Error())
	case errors.Is(err, errSnapshotInvalid):
		return newRequestError(id, api.ErrorInvalidPayload, err.Error())
	case errors.Is(err, errSnapshotTooLarge):
		return newRequestError(id, api.ErrorTooLarge, err.Error())
	default:
		return newRequestError(id, api.ErrorInternal, err.Error())
	}
}
