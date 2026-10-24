//go:build js && wasm

package main

import (
	"sync"
	"syscall/js"
)

type browserLocalStorage struct {
	mu       sync.Mutex
	fallback map[string][]byte
}

func newBrowserLocalStorage() *browserLocalStorage {
	return &browserLocalStorage{fallback: map[string][]byte{}}
}

func (s *browserLocalStorage) Load(key string) (value []byte, found bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			value, found = s.fallback[key]
			value, err = append([]byte(nil), value...), nil
		}
	}()
	storage := js.Global().Get("localStorage")
	if storage.IsNull() || storage.IsUndefined() {
		value, found = s.fallback[key]
		return append([]byte(nil), value...), found, nil
	}
	item := storage.Call("getItem", key)
	if item.IsNull() || item.IsUndefined() {
		value, found = s.fallback[key]
		return append([]byte(nil), value...), found, nil
	}
	return []byte(item.String()), true, nil
}

func (s *browserLocalStorage) Store(key string, value []byte) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			s.fallback[key], err = append([]byte(nil), value...), nil
		}
	}()
	storage := js.Global().Get("localStorage")
	if storage.IsNull() || storage.IsUndefined() {
		s.fallback[key] = append([]byte(nil), value...)
		return nil
	}
	storage.Call("setItem", key, string(value))
	s.fallback[key] = append([]byte(nil), value...)
	return nil
}
