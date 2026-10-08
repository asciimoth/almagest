// Package localconfig implements direct-mode fixed-target config persistence.
package localconfig

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/lib/logging"
)

const retainedResults = 128

type readTarget func() ([]byte, error)
type replaceTarget func([]byte) error

type receipt struct {
	expected string
	content  string
	result   daemonservices.ConfigOperationResult
}

// Persistence binds config operations to read and replace functions selected
// at startup. The functions do not accept paths, so callers cannot redirect a
// request to another file.
type Persistence struct {
	mu      sync.Mutex
	read    readTarget
	replace replaceTarget
	results map[string]receipt
	order   []string
}

type loggingPersistence struct {
	daemonservices.ConfigPersistence
	logger logging.Logger
}

// New constructs persistence for one fixed target.
func New(read func() ([]byte, error), replace func([]byte) error) *Persistence {
	return &Persistence{
		read: read, replace: replace, results: make(map[string]receipt),
	}
}

// WithLogger gives the local config-persistence role its own logger. The
// wrapper keeps the fixed-target contract and does not expose config contents.
func WithLogger(persistence daemonservices.ConfigPersistence, logger logging.Logger) daemonservices.ConfigPersistence {
	if persistence == nil || logger == nil {
		return persistence
	}
	return &loggingPersistence{ConfigPersistence: persistence, logger: logger}
}

func (p *loggingPersistence) ReadCurrent() (daemonservices.ConfigDocument, error) {
	document, err := p.ConfigPersistence.ReadCurrent()
	if err != nil {
		p.logger.Warnf("read current config failed: %v", err)
	}
	return document, err
}

func (p *loggingPersistence) Replace(expectedRevision, operationID string, data []byte) daemonservices.ConfigOperationResult {
	result := p.ConfigPersistence.Replace(expectedRevision, operationID, data)
	if err := result.Err(); err != nil {
		p.logger.Warnf("config replace failed: %v", err)
	} else {
		p.logger.Debug("config replace committed")
	}
	return result
}

func (p *loggingPersistence) GetResult(operationID string) (daemonservices.ConfigOperationResult, error) {
	result, err := p.ConfigPersistence.GetResult(operationID)
	if err != nil {
		p.logger.Warnf("config result lookup failed: %v", err)
	}
	return result, err
}

func (p *Persistence) ReadCurrent() (daemonservices.ConfigDocument, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readCurrent()
}

func (p *Persistence) readCurrent() (daemonservices.ConfigDocument, error) {
	if p == nil || p.read == nil {
		return daemonservices.ConfigDocument{}, errors.New("config persistence is not available")
	}
	data, err := p.read()
	if err != nil {
		return daemonservices.ConfigDocument{}, err
	}
	return daemonservices.ConfigDocument{
		Data: bytes.Clone(data), Revision: daemonservices.ConfigRevision(data),
	}, nil
}

func (p *Persistence) Replace(expectedRevision, operationID string, data []byte) daemonservices.ConfigOperationResult {
	p.mu.Lock()
	defer p.mu.Unlock()

	contentRevision := daemonservices.ConfigRevision(data)
	if operationID == "" {
		return daemonservices.ConfigOperationResult{Error: "config persistence operation ID is empty"}
	}
	if previous, ok := p.results[operationID]; ok {
		if previous.expected == expectedRevision && previous.content == contentRevision {
			return previous.result
		}
		return daemonservices.ConfigOperationResult{
			OperationID: operationID,
			Revision:    previous.result.Revision,
			Error:       "config persistence operation ID was reused with different input",
		}
	}

	current, err := p.readCurrent()
	if err != nil {
		return p.retain(expectedRevision, contentRevision, daemonservices.ConfigOperationResult{
			OperationID: operationID, Error: fmt.Sprintf("read current config: %v", err),
		})
	}
	if current.Revision != expectedRevision {
		return p.retain(expectedRevision, contentRevision, daemonservices.ConfigOperationResult{
			OperationID: operationID,
			Revision:    current.Revision,
			Error:       daemonservices.ErrConfigRevisionConflict.Error(),
		})
	}
	if p.replace == nil {
		return p.retain(expectedRevision, contentRevision, daemonservices.ConfigOperationResult{
			OperationID: operationID, Revision: current.Revision, Error: "config persistence is read-only",
		})
	}
	if err := p.replace(bytes.Clone(data)); err != nil {
		// Some platform replacements can report a post-commit validation or
		// durability error. Confirm the content before reporting failure, so a
		// caller does not retry an operation that already committed.
		after, readErr := p.readCurrent()
		if readErr == nil && current.Revision != contentRevision && after.Revision == contentRevision {
			return p.retain(expectedRevision, contentRevision, daemonservices.ConfigOperationResult{
				OperationID: operationID, Revision: contentRevision,
			})
		}
		revision := current.Revision
		if readErr == nil {
			revision = after.Revision
		}
		return p.retain(expectedRevision, contentRevision, daemonservices.ConfigOperationResult{
			OperationID: operationID, Revision: revision, Error: err.Error(),
		})
	}
	return p.retain(expectedRevision, contentRevision, daemonservices.ConfigOperationResult{
		OperationID: operationID, Revision: contentRevision,
	})
}

func (p *Persistence) GetResult(operationID string) (daemonservices.ConfigOperationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result, ok := p.results[operationID]
	if !ok {
		return daemonservices.ConfigOperationResult{}, daemonservices.ErrConfigOperationUnknown
	}
	return result.result, nil
}

func (p *Persistence) retain(expected, content string, result daemonservices.ConfigOperationResult) daemonservices.ConfigOperationResult {
	if len(p.order) == retainedResults {
		delete(p.results, p.order[0])
		copy(p.order, p.order[1:])
		p.order = p.order[:len(p.order)-1]
	}
	p.results[result.OperationID] = receipt{expected: expected, content: content, result: result}
	p.order = append(p.order, result.OperationID)
	return result
}
