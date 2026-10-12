// Package supervision owns isolated-role lifecycle and trusted host recovery.
// It is used only by the isolated composition. The direct daemon does not
// create role processes or recovery journals.
package supervision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/durablefile"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

const (
	journalVersion       = 1
	defaultMaxRecords    = 4096
	maxJournalBytes      = 8 * 1024 * 1024
	maxRecordStringBytes = 1024
)

var (
	ErrInvalidJournal    = errors.New("invalid isolation recovery journal")
	ErrJournalFull       = errors.New("isolation recovery journal is full")
	ErrRecoveryUncertain = errors.New("host recovery is uncertain")
)

// EffectState describes the strongest fact which N has durably recorded.
type EffectState string

const (
	EffectPending   EffectState = "pending"
	EffectOwned     EffectState = "owned"
	EffectUncertain EffectState = "uncertain"
)

// Effect records the minimum trusted information needed to inspect an effect.
// Identity is an opaque token issued by trusted native code. It must encode a
// stable native identity, not only a mutable interface or object name.
type Effect struct {
	ID              string                `json:"id"`
	SessionID       protocol.SessionID    `json:"session_id"`
	ResourceType    protocol.ResourceType `json:"resource_type"`
	Identity        string                `json:"identity,omitempty"`
	State           EffectState           `json:"state"`
	RetainOnFailure bool                  `json:"retain_on_failure,omitempty"`
}

func (e Effect) validate() error {
	if e.ID == "" || len(e.ID) > protocol.MaxOperationIDBytes || strings.ContainsAny(e.ID, "\x00\r\n") || !e.SessionID.Valid() {
		return ErrInvalidJournal
	}
	switch e.ResourceType {
	case protocol.ResourceTun, protocol.ResourceDefaultTun, protocol.ResourceSocket, protocol.ResourceListener, protocol.ResourceDNS:
	default:
		return ErrInvalidJournal
	}
	if len(e.Identity) > maxRecordStringBytes || strings.ContainsAny(e.Identity, "\x00\r\n") {
		return ErrInvalidJournal
	}
	switch e.State {
	case EffectPending:
		if e.Identity != "" {
			return ErrInvalidJournal
		}
	case EffectOwned, EffectUncertain:
		if e.Identity == "" {
			return ErrInvalidJournal
		}
	default:
		return ErrInvalidJournal
	}
	return nil
}

type journalDocument struct {
	Version int      `json:"version"`
	Records []Effect `json:"records"`
}

type journalEnvelope struct {
	Document json.RawMessage `json:"document"`
	SHA256   string          `json:"sha256"`
}

// Journal is a bounded durable ownership log in N's broker-private storage.
// A successful mutation must not start until its pending record is durable.
type Journal struct {
	mu      sync.Mutex
	path    string
	maximum int
	records map[string]Effect
}

// OpenJournal opens or creates the fixed journal target. Bootstrap must first
// verify that its parent is broker-private and outside R's writable storage.
func OpenJournal(path string, maximum int) (*Journal, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalidJournal
	}
	if maximum == 0 {
		maximum = defaultMaxRecords
	}
	if maximum < 1 || maximum > protocol.MaxLiveResources {
		return nil, ErrInvalidJournal
	}
	j := &Journal{path: path, maximum: maximum, records: make(map[string]Effect)}
	data, err := readJournalFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	records, err := decodeJournal(data, maximum)
	if err != nil {
		return nil, err
	}
	j.records = records
	return j, nil
}

func readJournalFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxJournalBytes {
		return nil, ErrInvalidJournal
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	return io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
}

func decodeJournal(data []byte, maximum int) (map[string]Effect, error) {
	if len(data) == 0 || len(data) > maxJournalBytes {
		return nil, ErrInvalidJournal
	}
	var envelope journalEnvelope
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || len(envelope.Document) == 0 {
		return nil, ErrInvalidJournal
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, ErrInvalidJournal
	}
	digest := sha256.Sum256(envelope.Document)
	if !strings.EqualFold(envelope.SHA256, hex.EncodeToString(digest[:])) {
		return nil, ErrInvalidJournal
	}
	var document journalDocument
	decoder = json.NewDecoder(strings.NewReader(string(envelope.Document)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Version != journalVersion || len(document.Records) > maximum {
		return nil, ErrInvalidJournal
	}
	if decoder.Decode(&trailing) != io.EOF {
		return nil, ErrInvalidJournal
	}
	records := make(map[string]Effect, len(document.Records))
	for _, record := range document.Records {
		if err := record.validate(); err != nil || records[record.ID].ID != "" {
			return nil, ErrInvalidJournal
		}
		records[record.ID] = record
	}
	return records, nil
}

// Put durably adds or updates an effect. Existing IDs cannot change session or
// resource type. This prevents a later operation from adopting old authority.
func (j *Journal) Put(effect Effect) error {
	if err := effect.validate(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if prior, ok := j.records[effect.ID]; ok {
		if prior.SessionID != effect.SessionID || prior.ResourceType != effect.ResourceType ||
			prior.Identity != "" && effect.Identity != prior.Identity || prior.RetainOnFailure != effect.RetainOnFailure {
			return ErrInvalidJournal
		}
	} else if len(j.records) >= j.maximum {
		return ErrJournalFull
	}
	next := cloneEffects(j.records)
	next[effect.ID] = effect
	if err := j.write(next); err != nil {
		return err
	}
	j.records = next
	return nil
}

// PrepareReplacement durably removes the current native identity while it
// keeps the resource record pending. A crash in the replacement window must
// require recovery; it must not treat the disappearance of the old identity
// as proof that the replacement left no host effects.
func (j *Journal) PrepareReplacement(effect Effect) error {
	if err := effect.validate(); err != nil || effect.State != EffectPending {
		return ErrInvalidJournal
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	prior, ok := j.records[effect.ID]
	if !ok || prior.SessionID != effect.SessionID || prior.ResourceType != effect.ResourceType ||
		prior.State != EffectOwned || prior.Identity == "" || prior.RetainOnFailure != effect.RetainOnFailure {
		return ErrInvalidJournal
	}
	next := cloneEffects(j.records)
	next[effect.ID] = effect
	if err := j.write(next); err != nil {
		return err
	}
	j.records = next
	return nil
}

// Resolve removes an effect only after trusted inspection proves that no owned
// host effect remains.
func (j *Journal) Resolve(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.records[id]; !ok {
		return nil
	}
	next := cloneEffects(j.records)
	delete(next, id)
	if err := j.write(next); err != nil {
		return err
	}
	j.records = next
	return nil
}

func cloneEffects(source map[string]Effect) map[string]Effect {
	result := make(map[string]Effect, len(source))
	for id, effect := range source {
		result[id] = effect
	}
	return result
}

func (j *Journal) write(records map[string]Effect) error {
	items := make([]Effect, 0, len(records))
	for _, record := range records {
		items = append(items, record)
	}
	slices.SortFunc(items, func(a, b Effect) int { return strings.Compare(a.ID, b.ID) })
	document, err := json.Marshal(journalDocument{Version: journalVersion, Records: items})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(document)
	data, err := json.Marshal(journalEnvelope{Document: document, SHA256: hex.EncodeToString(digest[:])})
	if err != nil || len(data) > maxJournalBytes {
		return errors.Join(ErrInvalidJournal, err)
	}
	directory := filepath.Dir(j.path)
	temporary, err := os.CreateTemp(directory, ".recovery-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := durablefile.Replace(j.path, temporaryPath); err != nil {
		return err
	}
	return syncDirectory(directory)
}

// Snapshot returns a stable copy for status and reconciliation.
func (j *Journal) Snapshot() []Effect {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]Effect, 0, len(j.records))
	for _, record := range j.records {
		result = append(result, record)
	}
	slices.SortFunc(result, func(a, b Effect) int { return strings.Compare(a.ID, b.ID) })
	return result
}

// IdentityState is the result of a trusted native identity comparison.
type IdentityState uint8

const (
	IdentityUnknown IdentityState = iota
	IdentityAbsent
	IdentityOwned
	IdentityForeign
)

// Reconciler inspects and removes host effects. Implementations must compare a
// native identity. They must not identify ownership from a name prefix.
type Reconciler interface {
	Inspect(context.Context, Effect) (IdentityState, error)
	Cleanup(context.Context, Effect) error
}

// RecoveryResult reports facts, including records deliberately retained for a
// strict failure policy. Remaining records always require operator visibility.
type RecoveryResult struct {
	Resolved  []string
	Remaining []Effect
}

// Recover reconciles all durable records. It never cleans a foreign or unknown
// object. RetainOnFailure effects remain installed until a later explicit
// policy-specific operation proves that removal is safe.
func (j *Journal) Recover(ctx context.Context, reconciler Reconciler, perEffect time.Duration) (RecoveryResult, error) {
	if reconciler == nil || perEffect <= 0 {
		return RecoveryResult{}, ErrInvalidJournal
	}
	var result RecoveryResult
	var joined error
	for _, effect := range j.Snapshot() {
		if err := ctx.Err(); err != nil {
			joined = errors.Join(joined, err)
			break
		}
		// A crash between intent persistence and native identity capture cannot
		// be repaired automatically. A name or operation ID is not ownership.
		if effect.State == EffectPending {
			joined = errors.Join(joined, fmt.Errorf("recover effect %s: %w", effect.ID, ErrRecoveryUncertain))
			continue
		}
		bounded, cancel := context.WithTimeout(ctx, perEffect)
		state, err := reconciler.Inspect(bounded, effect)
		if err == nil {
			err = bounded.Err()
		}
		if err == nil && state == IdentityAbsent {
			err = j.Resolve(effect.ID)
			if err == nil {
				result.Resolved = append(result.Resolved, effect.ID)
			}
		} else if err == nil && state == IdentityOwned && !effect.RetainOnFailure {
			err = reconciler.Cleanup(bounded, effect)
			if err == nil {
				err = bounded.Err()
			}
			if err == nil {
				state, err = reconciler.Inspect(bounded, effect)
				if err == nil {
					err = bounded.Err()
				}
				if err == nil && state == IdentityAbsent {
					err = j.Resolve(effect.ID)
					if err == nil {
						result.Resolved = append(result.Resolved, effect.ID)
					}
				} else if err == nil {
					err = ErrRecoveryUncertain
				}
			}
		} else if err == nil {
			err = ErrRecoveryUncertain
		}
		cancel()
		if err != nil {
			uncertain := effect
			uncertain.State = EffectUncertain
			if persistErr := j.Put(uncertain); persistErr != nil {
				err = errors.Join(err, fmt.Errorf("persist uncertain effect: %w", persistErr))
			}
			joined = errors.Join(joined, fmt.Errorf("recover effect %s: %w", effect.ID, err))
		}
	}
	result.Remaining = j.Snapshot()
	if len(result.Remaining) != 0 {
		joined = errors.Join(joined, ErrRecoveryUncertain)
	}
	return result, joined
}
