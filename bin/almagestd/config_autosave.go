package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/configer/hujson"
	tailscalehujson "github.com/tailscale/hujson"
)

const defaultConfigAutosaveIdle = 500 * time.Millisecond

var (
	errConfigInvalid          = errors.New("invalid editable config")
	errConfigRevisionConflict = errors.New("config revision conflict")
	errConfigProtectedContent = errors.New("editable config must not contain Protected")
)

type configWriter interface {
	WriteConfig(d []byte) error
}

type configAutosaver struct {
	engine almagest.Engine
	config *Config
	agent  configWriter
	logger logging.Logger
	idle   time.Duration

	mu     sync.Mutex
	saveMu sync.Mutex
	last   time.Time
	change uint64
	// These generations track idle autosave work separately from API revisions.
	scheduled uint64
	saved     uint64
	stopping  bool
	ch        chan struct{}
}

type nodeConfigAutosaveTrigger struct {
	mu      sync.Mutex
	pending []chan error
	stopped bool
	changes chan struct{}
}

func newNodeConfigAutosaveTrigger() *nodeConfigAutosaveTrigger {
	return &nodeConfigAutosaveTrigger{changes: make(chan struct{}, 1)}
}

func (t *nodeConfigAutosaveTrigger) Notify(uint64) <-chan error {
	result := make(chan error, 1)
	if t == nil {
		result <- errors.New("node configuration persistence is unavailable")
		close(result)
		return result
	}
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		result <- errors.New("node configuration persistence is stopped")
		close(result)
		return result
	}
	t.pending = append(t.pending, result)
	t.mu.Unlock()
	select {
	case t.changes <- struct{}{}:
	default:
	}
	return result
}

func (t *nodeConfigAutosaveTrigger) Run(ctx context.Context, saver *configAutosaver) {
	if t == nil || saver == nil {
		return
	}
	for {
		select {
		case <-t.changes:
			t.persistPending(ctx, saver)
		case <-ctx.Done():
			t.persistBatch(ctx, saver, t.stopAndTakePending())
			return
		}
	}
}

func (t *nodeConfigAutosaveTrigger) takePending() []chan error {
	t.mu.Lock()
	defer t.mu.Unlock()
	pending := t.pending
	t.pending = nil
	return pending
}

func (t *nodeConfigAutosaveTrigger) persistPending(ctx context.Context, saver *configAutosaver) {
	t.persistBatch(ctx, saver, t.takePending())
}

func (t *nodeConfigAutosaveTrigger) persistBatch(ctx context.Context, saver *configAutosaver, pending []chan error) {
	if len(pending) == 0 {
		return
	}
	var err error
	for {
		err = saver.persistNodeConfig()
		if err == nil || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(saver.idle)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
	for _, result := range pending {
		result <- err
		close(result)
	}
}

func (t *nodeConfigAutosaveTrigger) stopAndTakePending() []chan error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	pending := t.pending
	t.pending = nil
	return pending
}

func newConfigAutosaver(
	engine almagest.Engine,
	config *Config,
	agent configWriter,
	logger logging.Logger,
	idle time.Duration,
) *configAutosaver {
	if idle <= 0 {
		idle = defaultConfigAutosaveIdle
	}
	return &configAutosaver{
		engine: engine,
		config: config,
		agent:  agent,
		logger: logger,
		idle:   idle,
		ch:     make(chan struct{}, 1),
	}
}

func (s *configAutosaver) Run(ctx context.Context) {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.stopping = true
			s.mu.Unlock()
			if timer != nil {
				timer.Stop()
			}
			// Do not discard a graph mutation that is still in the idle window.
			s.flushPending()
			return
		case <-s.ch:
			if timer == nil {
				timer = time.NewTimer(s.idle)
				timerC = timer.C
				continue
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.idle)
		case <-timerC:
			if remaining := s.idleRemaining(); remaining > 0 {
				timer = time.NewTimer(remaining)
				timerC = timer.C
				continue
			}
			timerC = nil
			timer = nil
			if !s.save() && s.hasPending() {
				// Keep transient write failures pending without retrying in a tight loop.
				timer = time.NewTimer(s.idle)
				timerC = timer.C
			}
		}
	}
}

func (s *configAutosaver) Schedule() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.last = time.Now()
	s.change++
	s.scheduled++
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		s.flushPending()
		return
	}
	select {
	case s.ch <- struct{}{}:
	default:
	}
}

func (s *configAutosaver) idleRemaining() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := time.Until(s.last.Add(s.idle))
	if remaining <= 0 {
		return 0
	}
	return remaining
}

func (s *configAutosaver) save() bool {
	return s.persistNodeConfig() == nil
}

func (s *configAutosaver) persistNodeConfig() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	target := s.scheduledGeneration()

	if s.config == nil || s.config.Main == nil {
		return s.logSaveError(errors.New("config is not loaded"))
	}
	if s.engine == nil {
		return s.logSaveError(errors.New("engine is not available"))
	}
	if s.agent == nil {
		return s.logSaveError(errors.New("config writer is not available"))
	}
	data, err := s.currentConfigBytes()
	if err != nil {
		return s.logSaveError(fmt.Errorf("engine state export failed: %w", err))
	}
	if err := s.agent.WriteConfig(data); err != nil {
		return s.logSaveError(fmt.Errorf("disk write failed: %w", err))
	}
	s.markSaved(target)
	s.logger.Debug("config saved")
	return nil
}

func (s *configAutosaver) logSaveError(err error) error {
	if s != nil && s.logger != nil {
		s.logger.Errf("config save failed: %v", err)
	}
	return err
}

func (s *configAutosaver) flushPending() {
	_ = s.flushPendingError()
}

func (s *configAutosaver) flushPendingError() error {
	for s.hasPending() {
		if err := s.persistNodeConfig(); err != nil {
			return err
		}
	}
	return nil
}

func (s *configAutosaver) hasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduled != s.saved
}

func (s *configAutosaver) scheduledGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduled
}

func (s *configAutosaver) markSaved(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = generation
}

// Snapshot exports current live node state into the editable document before
// it returns the text. This prevents a client from editing stale graph data
// while a scheduled autosave is still waiting for its idle timer.
func (s *configAutosaver) Snapshot(readOnly bool) (api.ConfigSnapshot, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	// Keep the change counter stable while the engine state is exported. If a
	// concurrent API mutation finishes during the export, its Schedule call waits
	// here and makes this snapshot stale instead of assigning its revision to old
	// text.
	s.mu.Lock()
	data, err := s.currentConfigBytes()
	change := s.change
	s.mu.Unlock()
	if err != nil {
		return api.ConfigSnapshot{}, err
	}
	data, err = configOnlyBytes(data)
	if err != nil {
		return api.ConfigSnapshot{}, err
	}
	return newConfigSnapshot(data, readOnly, change), nil
}

// Update validates and applies an editable HuJSON document. The graph is
// reconciled before the new document becomes the autosave target. A successful
// call writes the config immediately instead of waiting for the idle timer.
func (s *configAutosaver) Update(text, revision string) (api.ConfigUpdateResponse, error) {
	next, err := parseEditableConfig([]byte(text))
	if err != nil {
		return api.ConfigUpdateResponse{}, err
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	storedData, err := s.storedConfigBytes()
	if err != nil {
		return api.ConfigUpdateResponse{}, err
	}
	currentData, err := configOnlyBytes(storedData)
	if err != nil {
		return api.ConfigUpdateResponse{}, err
	}
	current := newConfigSnapshot(currentData, false, s.changeRevision())
	if revision != "" && revision != current.Revision {
		return api.ConfigUpdateResponse{}, fmt.Errorf("%w: current revision is %s", errConfigRevisionConflict, current.Revision)
	}
	previous, err := parseEditableConfig(currentData)
	if err != nil {
		return api.ConfigUpdateResponse{}, fmt.Errorf("read current editable config: %w", err)
	}

	changes, err := s.engine.ReconcileConfig(next)
	if err != nil {
		return api.ConfigUpdateResponse{}, s.rollbackConfigReconcile(
			previous,
			fmt.Errorf("apply editable config: %w", err),
		)
	}

	// Export and write the local candidate before publishing it through Config.
	// This keeps readers on the previous document if persistence fails.
	if err := s.engine.SaveConfig(next); err != nil {
		return api.ConfigUpdateResponse{}, s.rollbackConfigReconcile(
			previous,
			fmt.Errorf("export reconciled config: %w", err),
		)
	}
	data, err := newConfigText(next)
	if err != nil {
		return api.ConfigUpdateResponse{}, s.rollbackConfigReconcile(previous, err)
	}
	if s.agent == nil {
		return api.ConfigUpdateResponse{}, s.rollbackConfigReconcile(
			previous,
			errors.New("config writer is not available"),
		)
	}
	if err := s.agent.WriteConfig(data); err != nil {
		applyErr := s.rollbackConfigReconcile(previous, fmt.Errorf("write config: %w", err))
		if restoreErr := s.agent.WriteConfig(storedData); restoreErr != nil {
			return api.ConfigUpdateResponse{}, fmt.Errorf("%w; restore previous config file: %v", applyErr, restoreErr)
		}
		return api.ConfigUpdateResponse{}, applyErr
	}
	s.config.mu.Lock()
	s.config.Main = next
	s.config.mu.Unlock()

	return api.ConfigUpdateResponse{
		Snapshot: newConfigSnapshot(data, false, s.changeRevision()),
		Changes:  changes,
	}, nil
}

// rollbackConfigReconcile restores the live graph after an update fails. The
// caller does not publish the candidate Config.Main until its file write has
// succeeded, so only the graph needs to be restored here.
func (s *configAutosaver) rollbackConfigReconcile(previous configer.Config, cause error) error {
	if _, rollbackErr := s.engine.ReconcileConfig(previous); rollbackErr != nil {
		return fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
	}
	return cause
}

func (s *configAutosaver) currentConfigBytes() ([]byte, error) {
	if s == nil || s.config == nil || s.engine == nil {
		return nil, errors.New("config autosaver is not ready")
	}
	s.config.mu.Lock()
	defer s.config.mu.Unlock()
	if s.config.Main == nil {
		return nil, errors.New("config is not loaded")
	}
	if err := s.engine.SaveConfig(s.config.Main); err != nil {
		return nil, err
	}
	return s.config.mainToBytesLocked()
}

func (s *configAutosaver) storedConfigBytes() ([]byte, error) {
	if s == nil || s.config == nil {
		return nil, errors.New("config autosaver is not ready")
	}
	return s.config.MainToBytes()
}

func (s *configAutosaver) changeRevision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.change
}

// noteRestoredConfig advances the revision after snapshot restore installs a
// complete config document. The caller must hold saveMu while it replaces
// Config.Main and rebuilds the engine.
func (s *configAutosaver) noteRestoredConfig() {
	s.mu.Lock()
	s.change++
	s.last = time.Now()
	s.mu.Unlock()
}

func parseEditableConfig(data []byte) (*hujson.Config, error) {
	cfg, err := hujson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errConfigInvalid, err)
	}
	root, err := cfg.Get(nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errConfigInvalid, err)
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: root must be an object", errConfigInvalid)
	}
	if _, err := cfg.Get(configer.Path{"Protected"}); err == nil {
		return nil, errConfigProtectedContent
	} else if !errors.Is(err, configer.ErrNotFound) {
		return nil, fmt.Errorf("%w: inspect Protected: %v", errConfigInvalid, err)
	}
	return cfg, nil
}

// configOnlyBytes defensively removes the legacy stdin-only Protected member
// from a copy of a document. Normal startup already splits that member before
// Config is built, but the API must keep this boundary even for injected test
// configs or future config sources.
func configOnlyBytes(data []byte) ([]byte, error) {
	cfg, err := hujson.Parse(data)
	if err != nil {
		return nil, err
	}
	for {
		err := cfg.Delete(configer.Path{"Protected"})
		switch {
		case err == nil:
			// HuJSON can contain duplicate object member names. Continue until all
			// protected members and their attached trivia are gone.
			continue
		case errors.Is(err, configer.ErrNotFound):
			return newConfigText(cfg)
		default:
			return nil, err
		}
	}
}

func newConfigText(cfg *hujson.Config) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("config is not loaded")
	}
	return tailscalehujson.Format(cfg.Pack())
}

func newConfigSnapshot(data []byte, readOnly bool, change uint64) api.ConfigSnapshot {
	return api.ConfigSnapshot{
		Text:     string(data),
		Revision: fmt.Sprintf("%x-%x", sha256.Sum256(data), change),
		ReadOnly: readOnly,
	}
}
