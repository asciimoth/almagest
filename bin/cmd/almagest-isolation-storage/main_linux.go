//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
)

const (
	storageRoot  = "/var/lib/almagest"
	configPath   = storageRoot + "/config/alm.hujson"
	protected    = "/etc/almagest/protected_alm.hujson"
	legacyConfig = "/etc/almagest/alm.hujson"
)

var layout = bootstrap.StorageLayout{Legacy: storageRoot + "/store", Runtime: storageRoot + "/runtime",
	Broker: storageRoot + "/broker", Backup: storageRoot + "/store.pre-isolation", State: storageRoot + "/isolation-migration.json"}

func run() error {
	if os.Geteuid() != 0 {
		return errors.New("isolation storage preparation must run as root")
	}
	if len(os.Args) != 2 {
		return errors.New("usage: almagest-isolation-storage prepare|recover|rollback")
	}
	identities, err := roleIdentities()
	if err != nil {
		return err
	}
	migrator := bootstrap.Migrator{}
	switch os.Args[1] {
	case "prepare":
		if err := prepareMigration(migrator); err != nil {
			return err
		}
		return prepareLayout(identities)
	case "recover":
		if err := migrator.Recover(layout); err != nil {
			return err
		}
		return prepareLayout(identities)
	case "rollback":
		return migrator.Rollback(layout)
	default:
		return errors.New("usage: almagest-isolation-storage prepare|recover|rollback")
	}
}

type identity struct{ uid, gid int }

func roleIdentities() (map[string]identity, error) {
	out := make(map[string]identity)
	for _, name := range []string{"almagest-runtime", "almagest-writer", "almagest-command"} {
		account, err := user.Lookup(name)
		if err != nil {
			return nil, fmt.Errorf("required isolation account %s: %w", name, err)
		}
		uid, uidErr := strconv.Atoi(account.Uid)
		gid, gidErr := strconv.Atoi(account.Gid)
		if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 {
			return nil, fmt.Errorf("required isolation account %s has an invalid identity", name)
		}
		out[name] = identity{uid: uid, gid: gid}
	}
	return out, nil
}

func prepareMigration(migrator bootstrap.Migrator) error {
	_, stateErr := os.Lstat(layout.State)
	if stateErr == nil {
		return migrator.Recover(layout)
	}
	if !errors.Is(stateErr, os.ErrNotExist) {
		return stateErr
	}
	_, legacyErr := os.Lstat(layout.Legacy)
	_, runtimeErr := os.Lstat(layout.Runtime)
	if legacyErr == nil && errors.Is(runtimeErr, os.ErrNotExist) {
		return migrator.Migrate(layout)
	}
	if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
		return legacyErr
	}
	if runtimeErr != nil && !errors.Is(runtimeErr, os.ErrNotExist) {
		return runtimeErr
	}
	if legacyErr == nil && runtimeErr == nil {
		return fmt.Errorf("both legacy and isolated runtime storage exist; stop and select recover or rollback")
	}
	return nil
}

func prepareLayout(identities map[string]identity) error {
	if err := os.MkdirAll(storageRoot, 0o755); err != nil {
		return err
	}
	if err := os.Chown(storageRoot, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(storageRoot, 0o755); err != nil {
		return err
	}
	for _, value := range []struct {
		path, owner string
		root        bool
	}{{filepath.Dir(configPath), "almagest-writer", false}, {layout.Runtime, "almagest-runtime", false},
		{storageRoot + "/helper", "almagest-command", false}, {layout.Broker, "", true}} {
		uid, gid := 0, 0
		if !value.root {
			uid, gid = identities[value.owner].uid, identities[value.owner].gid
		}
		if err := ensurePrivateTree(value.path, uid, gid); err != nil {
			return err
		}
	}
	if err := installRegularConfig(identities["almagest-writer"]); err != nil {
		return err
	}
	return installProtectedConfig()
}

func ensurePrivateTree(path string, uid, gid int) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe isolation directory %s", path)
	}
	if err := filepath.WalkDir(path, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !entry.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported runtime storage entry %s", entryPath)
		}
		return os.Lchown(entryPath, uid, gid)
	}); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func installRegularConfig(owner identity) error {
	if _, err := os.Lstat(configPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data := []byte("{}\n")
	if source, err := bootstrap.OpenTrustedFile(legacyConfig); err == nil {
		defer source.Close() //nolint:errcheck
		if read, readErr := io.ReadAll(io.LimitReader(source, (8<<20)+1)); readErr == nil && len(read) <= 8<<20 {
			data = read
		} else if readErr != nil {
			return readErr
		} else {
			return errors.New("legacy config exceeds 8 MiB")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		// A missing legacy file is a fresh installation. Any unsafe object at
		// that trusted path must stop preparation.
		if _, statErr := os.Lstat(legacyConfig); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return err
		}
	}
	return createOwnedFile(configPath, data, owner.uid, owner.gid)
}

func installProtectedConfig() error {
	if _, err := os.Lstat(protected); err == nil {
		file, openErr := bootstrap.OpenTrustedFile(protected)
		if file != nil {
			_ = file.Close()
		}
		return openErr
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(protected), 0o755); err != nil {
		return err
	}
	return createOwnedFile(protected, []byte("{\n  \"Admin\": {\"Unix\": \"/run/almagest/admin.sock\"},\n  \"System\": {\"Type\": \"native\"}\n}\n"), 0, 0)
}

func createOwnedFile(path string, data []byte, uid, gid int) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		_ = file.Close()
		if failed {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chown(uid, gid); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	failed = false
	return nil
}
