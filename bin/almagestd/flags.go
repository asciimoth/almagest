package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/durablefile"
)

const flagsFileName = "flags.json"
const flagsMaxBytes = 1 << 20

func readFlags(path string) (api.FlagsDocument, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return api.FlagsDocument{}, fmt.Errorf("open flags directory: %w", err)
	}
	defer root.Close() //nolint:errcheck
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return api.FlagsDocument{}, nil
	}
	if err != nil {
		return api.FlagsDocument{}, fmt.Errorf("stat flags file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > flagsMaxBytes {
		return api.FlagsDocument{}, fmt.Errorf("flags file must be a regular file of at most %d bytes", flagsMaxBytes)
	}
	file, err := root.Open(name)
	if err != nil {
		return api.FlagsDocument{}, fmt.Errorf("open flags file: %w", err)
	}
	defer file.Close() //nolint:errcheck
	openedInfo, err := file.Stat()
	if err != nil {
		return api.FlagsDocument{}, fmt.Errorf("stat opened flags file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return api.FlagsDocument{}, errors.New("flags file must be regular")
	}
	data, err := io.ReadAll(io.LimitReader(file, flagsMaxBytes+1))
	if err != nil {
		return api.FlagsDocument{}, fmt.Errorf("read flags file: %w", err)
	}
	if len(data) > flagsMaxBytes {
		return api.FlagsDocument{}, fmt.Errorf("flags file exceeds %d bytes", flagsMaxBytes)
	}
	document, err := decodeFlagsDocument(data)
	if err != nil {
		return api.FlagsDocument{}, fmt.Errorf("parse flags file: %w", err)
	}
	return document, nil
}

func decodeFlagsDocument(data []byte) (api.FlagsDocument, error) {
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return api.FlagsDocument{}, errors.New("flags must be a JSON object")
	}
	var document api.FlagsDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return api.FlagsDocument{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return api.FlagsDocument{}, errors.New("flags must contain one JSON object")
	}
	return document, nil
}

func cloneFlags(document api.FlagsDocument) api.FlagsDocument {
	copy := api.FlagsDocument{DocsOpened: document.DocsOpened, SnapshotsOpened: document.SnapshotsOpened, LaunchedBefore: document.LaunchedBefore}
	if document.Flags == nil {
		return copy
	}
	copy.Flags = make(map[string]bool, len(document.Flags))
	for name, enabled := range document.Flags {
		copy.Flags[name] = enabled
	}
	return copy
}

func cloneFlagOverrides(overrides map[string]bool) map[string]bool {
	copy := make(map[string]bool, len(overrides))
	for name, value := range overrides {
		copy[name] = value
	}
	return copy
}

func (m *bboltKVManager) applyFlagOverrides(document api.FlagsDocument) api.FlagsDocument {
	document = cloneFlags(document)
	if len(m.flagOverrides) > 0 && document.Flags == nil {
		document.Flags = make(map[string]bool, len(m.flagOverrides))
	}
	for name, value := range m.flagOverrides {
		document.Flags[name] = value
	}
	return document
}

func (m *bboltKVManager) flagsSnapshot() api.FlagsDocument {
	m.flagsMu.RLock()
	defer m.flagsMu.RUnlock()
	return cloneFlags(m.flags)
}

// reloadFlags runs during startup and while snapshot writers are quiesced.
func (m *bboltKVManager) reloadFlags() error {
	document, err := readFlags(m.flagsPath)
	if err != nil {
		return err
	}
	document = m.applyFlagOverrides(document)
	m.flagsMu.Lock()
	m.flags = document
	m.flagsMu.Unlock()
	return nil
}

func (m *bboltKVManager) replaceFlags(ctx context.Context, document api.FlagsDocument) error {
	if m.flagsPath == "" {
		return errors.New("flags storage is unavailable")
	}
	document = m.applyFlagOverrides(document)
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if len(data)+1 > flagsMaxBytes {
		return fmt.Errorf("flags file exceeds %d bytes", flagsMaxBytes)
	}
	release, err := m.writeBarrier.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	m.flagsMu.Lock()
	defer m.flagsMu.Unlock()
	return m.writeFlagsLocked(document, data)
}

func (m *bboltKVManager) markOpened(ctx context.Context, page string) (api.FlagsDocument, error) {
	if m.flagsPath == "" {
		return api.FlagsDocument{}, errors.New("flags storage is unavailable")
	}
	if page != "docs" && page != "snapshots" {
		return api.FlagsDocument{}, errors.New("unknown page")
	}
	release, err := m.writeBarrier.beginWrite(ctx)
	if err != nil {
		return api.FlagsDocument{}, err
	}
	defer release()
	m.flagsMu.Lock()
	defer m.flagsMu.Unlock()
	document := cloneFlags(m.flags)
	if page == "docs" {
		document.DocsOpened = true
	} else {
		document.SnapshotsOpened = true
	}
	data, err := json.Marshal(document)
	if err != nil {
		return api.FlagsDocument{}, err
	}
	if len(data)+1 > flagsMaxBytes {
		return api.FlagsDocument{}, fmt.Errorf("flags file exceeds %d bytes", flagsMaxBytes)
	}
	if err := m.writeFlagsLocked(document, data); err != nil {
		return api.FlagsDocument{}, err
	}
	return cloneFlags(document), nil
}

func (m *bboltKVManager) markLaunched(ctx context.Context) error {
	if m.flagsPath == "" {
		return errors.New("flags storage is unavailable")
	}
	release, err := m.writeBarrier.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	m.flagsMu.Lock()
	defer m.flagsMu.Unlock()
	document := cloneFlags(m.flags)
	if document.LaunchedBefore {
		return nil
	}
	document.LaunchedBefore = true
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if len(data)+1 > flagsMaxBytes {
		return fmt.Errorf("flags file exceeds %d bytes", flagsMaxBytes)
	}
	return m.writeFlagsLocked(document, data)
}

// writeFlagsLocked writes a complete document while flagsMu and the write barrier are held.
func (m *bboltKVManager) writeFlagsLocked(document api.FlagsDocument, data []byte) error {
	directory := filepath.Dir(m.flagsPath)
	file, err := os.CreateTemp(directory, ".flags-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name()) //nolint:errcheck
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(m.flagsPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("flags destination is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat flags destination: %w", err)
	}
	if err := durablefile.Replace(m.flagsPath, file.Name()); err != nil {
		return err
	}
	m.flags = cloneFlags(document)
	return syncSnapshotDirectory(directory)
}
