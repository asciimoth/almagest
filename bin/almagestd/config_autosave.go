package main

import (
	"context"
	"sync"
	"time"

	"github.com/asciimoth/almagest/lib"
)

const defaultConfigAutosaveIdle = 500 * time.Millisecond

type configWriter interface {
	WriteConfig(d []byte) error
}

type configAutosaver struct {
	engine almagest.Engine
	config *Config
	agent  configWriter
	logger almagest.Logger
	idle   time.Duration

	mu     sync.Mutex
	saveMu sync.Mutex
	last   time.Time
	ch     chan struct{}
}

func newConfigAutosaver(
	engine almagest.Engine,
	config *Config,
	agent configWriter,
	logger almagest.Logger,
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
			if timer != nil {
				timer.Stop()
			}
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
			s.save()
		}
	}
}

func (s *configAutosaver) Schedule() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
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

func (s *configAutosaver) save() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	if s.config == nil || s.config.Main == nil {
		s.logger.Err("config save failed: config is not loaded")
		return
	}
	if s.engine == nil {
		s.logger.Err("config save failed: engine is not available")
		return
	}
	if s.agent == nil {
		s.logger.Err("config save failed: config writer is not available")
		return
	}
	if err := s.engine.SaveConfig(s.config.Main); err != nil {
		s.logger.Errf("config save failed: engine state export failed: %v", err)
		return
	}
	data, err := s.config.MainToBytes()
	if err != nil {
		s.logger.Errf("config save failed: %v", err)
	}
	if err := s.agent.WriteConfig(data); err != nil {
		s.logger.Errf("config save failed: disk write failed: %v", err)
		return
	}
	s.logger.Debug("config saved")
}
