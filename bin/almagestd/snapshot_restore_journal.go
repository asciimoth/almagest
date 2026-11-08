package main

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
	"strings"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/durablefile"
)

const (
	restoreJournalVersion = 1
	restoreJournalName    = ".live-restore-journal.json"
	restoreRecoveryName   = ".live-restore-recovery.zip"
	restoreJournalMaxSize = 64 << 10
)

var errRestoreRecoveryRequired = errors.New("snapshot restore recovery is required")

type restoreJournalState string

const (
	restoreStatePrepared    restoreJournalState = "prepared"
	restoreStateDataApplied restoreJournalState = "data-applied"
	restoreStateCommitted   restoreJournalState = "committed"
)

// restoreJournal contains application facts only. In particular, it contains
// no broker resource ID, native identity, session, or graph epoch.
type restoreJournal struct {
	Version              int                 `json:"version"`
	State                restoreJournalState `json:"state"`
	SnapshotUUID         string              `json:"snapshot_uuid"`
	RecoverySHA256       string              `json:"recovery_sha256"`
	RestoredConfigSHA256 string              `json:"restored_config_sha256"`
}

func (j restoreJournal) validate() error {
	if j.Version != restoreJournalVersion {
		return errRestoreRecoveryRequired
	}
	if _, err := normalizeSnapshotUUID(j.SnapshotUUID); err != nil {
		return errRestoreRecoveryRequired
	}
	switch j.State {
	case restoreStatePrepared, restoreStateDataApplied, restoreStateCommitted:
	default:
		return errRestoreRecoveryRequired
	}
	if !validSHA256(j.RecoverySHA256) || !validSHA256(j.RestoredConfigSHA256) {
		return errRestoreRecoveryRequired
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func restoreStatePaths(storagePath string) (journal, recovery string) {
	directory := filepath.Join(storagePath, snapshotDirectoryName)
	return filepath.Join(directory, restoreJournalName), filepath.Join(directory, restoreRecoveryName)
}

func readRestoreJournal(storagePath string) (restoreJournal, error) {
	journalPath, _ := restoreStatePaths(storagePath)
	info, err := os.Lstat(journalPath)
	if err != nil {
		return restoreJournal{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > restoreJournalMaxSize {
		return restoreJournal{}, errRestoreRecoveryRequired
	}
	file, err := os.Open(journalPath)
	if err != nil {
		return restoreJournal{}, err
	}
	defer file.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(file, restoreJournalMaxSize+1))
	if err != nil {
		return restoreJournal{}, err
	}
	return decodeRestoreJournal(data)
}

func decodeRestoreJournal(data []byte) (restoreJournal, error) {
	if len(data) == 0 || len(data) > restoreJournalMaxSize {
		return restoreJournal{}, errRestoreRecoveryRequired
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var journal restoreJournal
	if err := decoder.Decode(&journal); err != nil {
		return restoreJournal{}, errors.Join(errRestoreRecoveryRequired, err)
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return restoreJournal{}, errRestoreRecoveryRequired
	}
	if err := journal.validate(); err != nil {
		return restoreJournal{}, err
	}
	return journal, nil
}

func writeRestoreJournal(storagePath string, journal restoreJournal) error {
	if err := journal.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(journal)
	if err != nil || len(data) > restoreJournalMaxSize {
		return errors.Join(errRestoreRecoveryRequired, err)
	}
	data = append(data, '\n')
	journalPath, _ := restoreStatePaths(storagePath)
	directory := filepath.Dir(journalPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".restore-journal-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer removeFileIfPresent(temporaryPath)
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
	if err := durablefile.Replace(journalPath, temporaryPath); err != nil {
		return err
	}
	return syncSnapshotDirectory(directory)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close() //nolint:errcheck
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, api.SnapshotArchiveMaxBytes+1)); err != nil {
		return "", err
	}
	if info, err := file.Stat(); err != nil {
		return "", err
	} else if info.Size() > api.SnapshotArchiveMaxBytes {
		return "", errSnapshotTooLarge
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type interruptedRestoreResult struct {
	ConfigData            []byte
	RolledBack            bool
	UnresolvedConfigWrite bool
}

// recoverInterruptedRestore runs before bbolt and the engine open. A
// pre-commit journal restores the recovery archive. A committed journal never
// rolls application storage back.
func recoverInterruptedRestore(
	ctx context.Context,
	storagePath string,
	regularConfig []byte,
	locator any,
) (interruptedRestoreResult, error) {
	journal, err := readRestoreJournal(storagePath)
	if errors.Is(err, os.ErrNotExist) {
		return interruptedRestoreResult{}, nil
	}
	if err != nil {
		return interruptedRestoreResult{}, fmt.Errorf("%w: read restore journal: %v", errRestoreRecoveryRequired, err)
	}
	if journal.State == restoreStateCommitted {
		if err := removeCommittedRecovery(storagePath); err != nil {
			return interruptedRestoreResult{}, fmt.Errorf("%w: remove committed recovery copy: %v", errRestoreRecoveryRequired, err)
		}
		if configSHA256(regularConfig) == journal.RestoredConfigSHA256 {
			if err := clearRestoreState(storagePath, true); err != nil {
				return interruptedRestoreResult{}, fmt.Errorf("%w: clear committed restore journal: %v", errRestoreRecoveryRequired, err)
			}
			return interruptedRestoreResult{}, nil
		}
		return interruptedRestoreResult{UnresolvedConfigWrite: true}, nil
	}

	recoveryPath, err := verifyRecoveryArchive(storagePath, journal.RecoverySHA256)
	if err != nil {
		return interruptedRestoreResult{}, err
	}
	recovery, err := os.Open(recoveryPath)
	if err != nil {
		return interruptedRestoreResult{}, errors.Join(errRestoreRecoveryRequired, err)
	}
	configData, _, restoreErr := restoreStartupSnapshotWithMode(ctx, storagePath, recovery, locator, true)
	closeErr := recovery.Close()
	if restoreErr != nil || closeErr != nil {
		return interruptedRestoreResult{}, fmt.Errorf("%w: restore recovery copy: %v", errRestoreRecoveryRequired, errors.Join(restoreErr, closeErr))
	}
	if err := clearRestoreState(storagePath, false); err != nil {
		return interruptedRestoreResult{}, fmt.Errorf("%w: finish interrupted rollback: %v", errRestoreRecoveryRequired, err)
	}
	return interruptedRestoreResult{ConfigData: configData, RolledBack: true}, nil
}

func verifyRecoveryArchive(storagePath, expected string) (string, error) {
	_, recoveryPath := restoreStatePaths(storagePath)
	info, err := os.Lstat(recoveryPath)
	if err != nil {
		return "", errors.Join(errRestoreRecoveryRequired, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > api.SnapshotArchiveMaxBytes {
		return "", fmt.Errorf("%w: recovery archive is not a bounded regular file", errRestoreRecoveryRequired)
	}
	actual, err := fileSHA256(recoveryPath)
	if err != nil {
		return "", errors.Join(errRestoreRecoveryRequired, err)
	}
	if actual != expected {
		return "", fmt.Errorf("%w: recovery archive checksum does not match", errRestoreRecoveryRequired)
	}
	return recoveryPath, nil
}

func configSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func clearRestoreState(storagePath string, committed bool) error {
	journalPath, recoveryPath := restoreStatePaths(storagePath)
	// After rollback, remove the journal first. A crash then sees old storage as
	// complete. After commit, remove recovery first because rollback is forbidden.
	paths := []string{journalPath, recoveryPath}
	if committed {
		paths[0], paths[1] = paths[1], paths[0]
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncSnapshotDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}

func removeCommittedRecovery(storagePath string) error {
	_, recoveryPath := restoreStatePaths(storagePath)
	if err := os.Remove(recoveryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncSnapshotDirectory(filepath.Dir(recoveryPath))
}
