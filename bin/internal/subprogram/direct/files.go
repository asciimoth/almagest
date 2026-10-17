package direct

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	sandbox "github.com/asciimoth/gobox"
)

type confinedRoot struct {
	mu     sync.Mutex
	root   *os.Root
	closed bool
	open   int
}

func openConfinedChild(parent *os.Root, name string) (*confinedRoot, os.FileInfo, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, sandbox.ErrDenied
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open storage: %v", sandbox.ErrUnavailable, err)
	}
	held, err := safeRootOpen(root, ".")
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	heldInfo, statErr := held.Stat()
	closeErr := held.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(info, heldInfo) {
		_ = root.Close()
		return nil, nil, errors.Join(sandbox.ErrDenied, statErr, closeErr)
	}
	return &confinedRoot{root: root}, heldInfo, nil
}

func (r *confinedRoot) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if r.open != 0 {
		return fmt.Errorf("%w: %d open files", sandbox.ErrClosed, r.open)
	}
	r.closed = true
	return r.root.Close()
}

func (r *confinedRoot) validate(name string, allowRoot bool) (string, error) {
	if err := sandbox.ValidatePath(sandbox.Path{Root: sandbox.Temporary, Rel: name}, allowRoot); err != nil {
		return "", err
	}
	if name == "" {
		name = "."
	}
	current := ""
	parts := strings.Split(name, "/")
	for index, part := range parts {
		if part == "." {
			continue
		}
		current = path.Join(current, part)
		info, err := r.root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) && index == len(parts)-1 {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || index < len(parts)-1 && !info.IsDir() {
			return "", sandbox.ErrDenied
		}
		opened, err := safeRootOpen(r.root, current)
		if err != nil {
			return "", err
		}
		openedInfo, statErr := opened.Stat()
		links := false
		if statErr == nil {
			links = openedFileHasMultipleLinks(opened, openedInfo)
		}
		closeErr := opened.Close()
		if statErr != nil {
			return "", statErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if !os.SameFile(info, openedInfo) || links {
			return "", sandbox.ErrDenied
		}
	}
	return name, nil
}

type files struct {
	instance *instance
	root     *confinedRoot
	rootID   sandbox.Root
}

var _ sandbox.Files = (*files)(nil)

func (f *files) withLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-f.instance.ctx.Done():
		return sandbox.ErrClosed
	default:
	}
	f.root.mu.Lock()
	defer f.root.mu.Unlock()
	if f.root.closed {
		return sandbox.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (f *files) MkdirAll(ctx context.Context, name string, mode fs.FileMode) error {
	if mode & ^fs.FileMode(0o777) != 0 {
		return sandbox.ErrInvalid
	}
	return f.withLock(ctx, func() error {
		name, err := f.root.validate(name, false)
		if err != nil {
			return err
		}
		if info, statErr := f.root.root.Lstat(name); statErr == nil && info.IsDir() {
			return nil
		}
		_, entries, err := rootUsage(f.root.root, ".", storageEntryLimit(f.instance))
		if err != nil {
			return err
		}
		missing, err := missingEntryCount(f.root.root, name)
		if err != nil {
			return err
		}
		if entries+missing > storageEntryLimit(f.instance) {
			return sandbox.ErrQuota
		}
		return f.root.root.MkdirAll(name, mode.Perm())
	})
}

func (f *files) TempDir(ctx context.Context, directory, pattern string) (string, error) {
	if len(pattern) > 128 || strings.ContainsAny(pattern, `/\`) || strings.ContainsRune(pattern, 0) {
		return "", sandbox.ErrInvalid
	}
	var result string
	err := f.withLock(ctx, func() error {
		directory, err := f.root.validate(directory, true)
		if err != nil {
			return err
		}
		_, entries, usageErr := rootUsage(f.root.root, ".", storageEntryLimit(f.instance))
		if usageErr != nil {
			return usageErr
		}
		if entries >= storageEntryLimit(f.instance) {
			return sandbox.ErrQuota
		}
		for attempt := range 100 {
			name := fmt.Sprintf("%s%016x", pattern, randomUint64(uint64(attempt)))
			candidate := path.Join(directory, name)
			err = f.root.root.Mkdir(candidate, 0o700)
			if err == nil {
				result = candidate
				return nil
			}
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
		}
		return sandbox.ErrQuota
	})
	return result, err
}

func randomUint64(salt uint64) uint64 {
	var data [8]byte
	if _, err := rand.Read(data[:]); err == nil {
		var result uint64
		for _, value := range data {
			result = result<<8 | uint64(value)
		}
		return result
	}
	return salt
}

func readBounded(file fs.File, maximum int64) ([]byte, error) {
	if maximum == 0 {
		return []byte{}, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, sandbox.ErrQuota
	}
	return data, nil
}

func (f *files) Open(ctx context.Context, name string, options sandbox.OpenOptions) (sandbox.File, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	var result *trackedFile
	err := f.withLock(ctx, func() error {
		name, err := f.root.validate(name, false)
		if err != nil {
			return err
		}
		if f.root.open >= f.instance.template.Limits.Files {
			return sandbox.ErrQuota
		}
		flags := 0
		switch {
		case options.Read && options.Write:
			flags |= os.O_RDWR
		case options.Write:
			flags |= os.O_WRONLY
		default:
			flags |= os.O_RDONLY
		}
		if options.Create {
			if _, statErr := f.root.root.Lstat(name); statErr != nil {
				if !errors.Is(statErr, fs.ErrNotExist) {
					return statErr
				}
				_, entries, usageErr := rootUsage(f.root.root, ".", storageEntryLimit(f.instance))
				if usageErr != nil {
					return usageErr
				}
				if entries >= storageEntryLimit(f.instance) {
					return sandbox.ErrQuota
				}
			}
			flags |= os.O_CREATE
		}
		if options.Exclusive {
			flags |= os.O_EXCL
		}
		if options.Append {
			flags |= os.O_APPEND
		}
		file, err := safeRootOpenFile(f.root.root, name, flags, options.Mode.Perm())
		if err != nil {
			return err
		}
		info, err := file.Stat()
		current, pathErr := f.root.root.Lstat(name)
		if err != nil || pathErr != nil || current.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(current, info) || openedFileHasMultipleLinks(file, info) {
			_ = file.Close()
			if err != nil || pathErr != nil {
				return errors.Join(err, pathErr)
			}
			return sandbox.ErrDenied
		}
		if options.Truncate {
			if err := file.Truncate(0); err != nil {
				_ = file.Close()
				return err
			}
		}
		f.root.open++
		result = &trackedFile{File: file, root: f.root, owner: f.instance, rootID: f.rootID, name: name, size: info.Size(), append: options.Append}
		if err := f.instance.trackFile(result); err != nil {
			f.root.open--
			_ = file.Close()
			return err
		}
		return nil
	})
	return result, err
}

func (f *files) ReadFile(ctx context.Context, name string, maximum int64) ([]byte, error) {
	if maximum < 0 || maximum > f.instance.template.Limits.TransferBytes {
		return nil, sandbox.ErrQuota
	}
	file, err := f.Open(ctx, name, sandbox.OpenOptions{Read: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := readBounded(file, maximum)
	return data, err
}

func (f *files) WriteFile(ctx context.Context, name string, data []byte, mode fs.FileMode) error {
	if int64(len(data)) > f.instance.template.Limits.TransferBytes {
		return sandbox.ErrQuota
	}
	if mode&^fs.FileMode(0o777) != 0 {
		return sandbox.ErrInvalid
	}
	return f.withLock(ctx, func() error {
		name, err := f.root.validate(name, false)
		if err != nil {
			return err
		}
		created := false
		held, openErr := safeRootOpenFile(f.root.root, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if errors.Is(openErr, fs.ErrExist) {
			held, openErr = safeRootOpenFile(f.root.root, name, os.O_WRONLY, 0)
		} else if openErr == nil {
			created = true
		}
		if openErr != nil {
			return openErr
		}
		closed := false
		keepCreated := false
		var openedInfo os.FileInfo
		defer func() {
			if !closed {
				_ = held.Close()
			}
			if created && !keepCreated {
				current, statErr := f.root.root.Lstat(name)
				if statErr == nil && openedInfo != nil && os.SameFile(current, openedInfo) {
					_ = f.root.root.Remove(name)
				}
			}
		}()
		heldInfo, heldErr := held.Stat()
		openedInfo = heldInfo
		current, pathErr := f.root.root.Lstat(name)
		if heldErr != nil || pathErr != nil {
			return errors.Join(heldErr, pathErr)
		}
		if current.Mode()&os.ModeSymlink != 0 || !heldInfo.Mode().IsRegular() || !os.SameFile(current, heldInfo) || openedFileHasMultipleLinks(held, heldInfo) {
			return sandbox.ErrDenied
		}
		if f.rootID == sandbox.Temporary {
			usage, _, usageErr := rootUsage(f.root.root, ".", storageEntryLimit(f.instance))
			if usageErr != nil {
				return usageErr
			}
			if usage-heldInfo.Size()+int64(len(data)) > f.instance.template.Limits.TemporaryBytes {
				return sandbox.ErrQuota
			}
		}
		if err := held.Truncate(0); err != nil {
			return err
		}
		if _, err := held.Write(data); err != nil {
			return err
		}
		closeErr := held.Close()
		closed = true
		keepCreated = closeErr == nil
		return closeErr
	})
}

func (f *files) Rename(ctx context.Context, oldName, newName string) error {
	return f.withLock(ctx, func() error {
		oldName, err := f.root.validate(oldName, false)
		if err != nil {
			return err
		}
		newName, err = f.root.validate(newName, false)
		if err != nil {
			return err
		}
		return f.root.root.Rename(oldName, newName)
	})
}

func (f *files) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	var result fs.FileInfo
	err := f.withLock(ctx, func() error {
		name, err := f.root.validate(name, true)
		if err != nil {
			return err
		}
		info, err := f.root.root.Lstat(name)
		if err != nil {
			return err
		}
		held, err := safeRootOpen(f.root.root, name)
		if err != nil {
			return err
		}
		heldInfo, statErr := held.Stat()
		aliases := statErr == nil && openedFileHasMultipleLinks(held, heldInfo)
		closeErr := held.Close()
		if statErr != nil || closeErr != nil {
			return errors.Join(statErr, closeErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, heldInfo) || aliases {
			return sandbox.ErrDenied
		}
		result = sanitizedInfo{info: heldInfo}
		return nil
	})
	return result, err
}

func (f *files) ReadDir(ctx context.Context, name string, maximum int) ([]fs.DirEntry, error) {
	if maximum < 1 || maximum > 4096 {
		return nil, sandbox.ErrQuota
	}
	var result []fs.DirEntry
	err := f.withLock(ctx, func() error {
		name, err := f.root.validate(name, true)
		if err != nil {
			return err
		}
		file, err := safeRootOpen(f.root.root, name)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		heldInfo, err := file.Stat()
		current, pathErr := f.root.root.Lstat(name)
		if err != nil || pathErr != nil {
			return errors.Join(err, pathErr)
		}
		if current.Mode()&os.ModeSymlink != 0 || !heldInfo.IsDir() || !os.SameFile(current, heldInfo) {
			return sandbox.ErrDenied
		}
		entries, err := file.ReadDir(maximum + 1)
		if err != nil {
			return err
		}
		if len(entries) > maximum {
			return sandbox.ErrQuota
		}
		result = make([]fs.DirEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return sandbox.ErrDenied
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			held, err := safeRootOpen(f.root.root, path.Join(name, entry.Name()))
			if err != nil {
				return err
			}
			heldInfo, statErr := held.Stat()
			links := statErr == nil && openedFileHasMultipleLinks(held, heldInfo)
			closeErr := held.Close()
			if statErr != nil {
				return statErr
			}
			if closeErr != nil {
				return closeErr
			}
			if !os.SameFile(info, heldInfo) || links {
				return sandbox.ErrDenied
			}
			result = append(result, sanitizedEntry{name: entry.Name(), info: sanitizedInfo{info: info}})
		}
		return nil
	})
	return result, err
}

func (f *files) Remove(ctx context.Context, name string) error {
	return f.withLock(ctx, func() error {
		name, err := f.root.validate(name, false)
		if err != nil {
			return err
		}
		return f.root.root.Remove(name)
	})
}

func (f *files) RemoveAll(ctx context.Context, name string) error {
	return f.withLock(ctx, func() error {
		name, err := f.root.validate(name, false)
		if err != nil {
			return err
		}
		return f.root.root.RemoveAll(name)
	})
}

type trackedFile struct {
	*os.File
	root   *confinedRoot
	owner  *instance
	rootID sandbox.Root
	name   string
	size   int64
	append bool
	once   sync.Once
}

func (f *trackedFile) Close() error {
	var result error
	f.once.Do(func() {
		f.root.mu.Lock()
		result = f.File.Close()
		f.root.open--
		f.root.mu.Unlock()
		f.owner.forgetFile(f)
	})
	return result
}

func (f *trackedFile) Write(data []byte) (int, error) {
	if int64(len(data)) > f.owner.template.Limits.TransferBytes {
		return 0, sandbox.ErrQuota
	}
	f.root.mu.Lock()
	defer f.root.mu.Unlock()
	select {
	case <-f.owner.ctx.Done():
		return 0, sandbox.ErrClosed
	default:
	}
	position, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	info, err := f.File.Stat()
	if err != nil {
		return 0, err
	}
	currentSize := max(f.size, info.Size())
	pathInfo, err := f.root.root.Lstat(f.name)
	if err != nil {
		return 0, err
	}
	if !os.SameFile(pathInfo, info) {
		return 0, sandbox.ErrDenied
	}
	if f.rootID == sandbox.Temporary {
		growth := position + int64(len(data)) - currentSize
		if f.append {
			growth = int64(len(data))
		}
		if growth < 0 {
			growth = 0
		}
		usage, _, usageErr := rootUsageWithSize(f.root.root, storageEntryLimit(f.owner), f.name, currentSize)
		if usageErr != nil {
			return 0, usageErr
		}
		if usage+growth > f.owner.template.Limits.TemporaryBytes {
			return 0, sandbox.ErrQuota
		}
	}
	written, writeErr := f.File.Write(data)
	if f.append {
		f.size = currentSize + int64(written)
	} else {
		f.size = max(currentSize, position+int64(written))
	}
	return written, writeErr
}
func (f *trackedFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return sanitizedInfo{info: info}, nil
}

func storageEntryLimit(owner *instance) int {
	return min(max(owner.template.Limits.Files*16, 1024), 100000)
}

func missingEntryCount(root *os.Root, name string) (int, error) {
	parts := strings.Split(name, "/")
	for index := range parts {
		candidate := path.Join(parts[:index+1]...)
		info, err := root.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return len(parts) - index, nil
		}
		if err != nil {
			return 0, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) {
			return 0, sandbox.ErrDenied
		}
	}
	return 0, nil
}

func rootUsage(root *os.Root, directory string, maximumEntries int) (int64, int, error) {
	entries := 0
	usage, err := rootUsageBelow(root, directory, maximumEntries, &entries, "", 0)
	return usage, entries, err
}

func rootUsageWithSize(root *os.Root, maximumEntries int, name string, size int64) (int64, int, error) {
	entries := 0
	usage, err := rootUsageBelow(root, ".", maximumEntries, &entries, name, size)
	return usage, entries, err
}

func rootUsageBelow(root *os.Root, directory string, maximumEntries int, entryCount *int, overrideName string, overrideSize int64) (int64, error) {
	file, err := safeRootOpen(root, directory)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	heldInfo, err := file.Stat()
	current, pathErr := root.Lstat(directory)
	if err != nil || pathErr != nil {
		return 0, errors.Join(err, pathErr)
	}
	if current.Mode()&os.ModeSymlink != 0 || !heldInfo.IsDir() || !os.SameFile(current, heldInfo) {
		return 0, sandbox.ErrDenied
	}
	entries, err := file.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		(*entryCount)++
		if *entryCount > maximumEntries {
			return 0, sandbox.ErrQuota
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return 0, sandbox.ErrDenied
		}
		name := path.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		held, err := safeRootOpen(root, name)
		if err != nil {
			return 0, err
		}
		heldInfo, statErr := held.Stat()
		links := statErr == nil && openedFileHasMultipleLinks(held, heldInfo)
		closeErr := held.Close()
		if statErr != nil {
			return 0, statErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if !os.SameFile(info, heldInfo) || links {
			return 0, sandbox.ErrDenied
		}
		if info.IsDir() {
			value, err := rootUsageBelow(root, name, maximumEntries, entryCount, overrideName, overrideSize)
			if err != nil {
				return 0, err
			}
			total += value
			continue
		}
		if !info.Mode().IsRegular() || hasMultipleLinks(info) {
			return 0, sandbox.ErrDenied
		}
		if name == overrideName {
			total += overrideSize
		} else {
			total += info.Size()
		}
	}
	return total, nil
}

type sanitizedInfo struct{ info fs.FileInfo }

func (i sanitizedInfo) Name() string               { return i.info.Name() }
func (i sanitizedInfo) Size() int64                { return i.info.Size() }
func (i sanitizedInfo) Mode() fs.FileMode          { return i.info.Mode() }
func (i sanitizedInfo) ModTime() (value time.Time) { return value }
func (i sanitizedInfo) IsDir() bool                { return i.info.IsDir() }
func (i sanitizedInfo) Sys() any                   { return nil }

type sanitizedEntry struct {
	name string
	info sanitizedInfo
}

func (e sanitizedEntry) Name() string               { return e.name }
func (e sanitizedEntry) IsDir() bool                { return e.info.IsDir() }
func (e sanitizedEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e sanitizedEntry) Info() (fs.FileInfo, error) { return e.info, nil }
