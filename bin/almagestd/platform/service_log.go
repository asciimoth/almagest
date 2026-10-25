package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	WindowsServiceLogMaxBytes = 20 * 1024 * 1024
	WindowsServiceLogBackups  = 5
)

var errRotatingLogClosed = errors.New("rotating log is closed")

type rotatingLogFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	backups int
	file    *os.File
	size    int64
	open    func(string) (*os.File, error)
	rename  func(string, string) error
	remove  func(string) error
	closed  bool
}

func newRotatingLogFile(path string, maxSize int64, backups int, open func(string) (*os.File, error)) (*rotatingLogFile, error) {
	if path == "" || maxSize <= 0 || backups < 0 || open == nil {
		return nil, errors.New("invalid rotating log configuration")
	}
	file, err := open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("service log is not a regular file: %s", path)
	}
	return &rotatingLogFile{
		path: path, maxSize: maxSize, backups: backups, file: file, size: info.Size(),
		open: open, rename: os.Rename, remove: os.Remove,
	}, nil
}

func (w *rotatingLogFile) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errRotatingLogClosed
	}
	written := 0
	for len(data) > 0 {
		if w.size >= w.maxSize {
			if err := w.rotate(); err != nil {
				return written, err
			}
		}
		chunk := data
		if room := w.maxSize - w.size; int64(len(chunk)) > room {
			chunk = chunk[:room]
		}
		n, err := w.file.Write(chunk)
		written += n
		w.size += int64(n)
		data = data[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (w *rotatingLogFile) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close service log for rotation: %w", err)
	}
	w.file = nil
	reopen := func(cause error) error {
		file, openErr := w.open(w.path)
		if openErr == nil {
			w.file = file
			if info, statErr := file.Stat(); statErr == nil {
				w.size = info.Size()
			} else {
				openErr = statErr
			}
		}
		return errors.Join(cause, openErr)
	}
	if w.backups == 0 {
		if err := w.remove(w.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return reopen(fmt.Errorf("remove full service log: %w", err))
		}
	} else {
		for index := w.backups; index >= 2; index-- {
			source := fmt.Sprintf("%s.%d", w.path, index-1)
			destination := fmt.Sprintf("%s.%d", w.path, index)
			if err := w.remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				return reopen(fmt.Errorf("remove oldest service log: %w", err))
			}
			if err := w.rename(source, destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				return reopen(fmt.Errorf("rotate service log backup: %w", err))
			}
		}
		first := w.path + ".1"
		if err := w.remove(first); err != nil && !errors.Is(err, os.ErrNotExist) {
			return reopen(fmt.Errorf("remove first service log backup: %w", err))
		}
		if err := w.rename(w.path, first); err != nil && !errors.Is(err, os.ErrNotExist) {
			return reopen(fmt.Errorf("rotate service log: %w", err))
		}
	}
	file, err := w.open(w.path)
	if err != nil {
		return fmt.Errorf("open service log after rotation: %w", err)
	}
	w.file = file
	w.size = 0
	return nil
}

func (w *rotatingLogFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file == nil {
		return nil
	}
	return errors.Join(w.file.Sync(), w.file.Close())
}
