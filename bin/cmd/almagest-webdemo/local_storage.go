//go:build js && wasm

package main

import (
	"fmt"
	"syscall/js"
)

type browserLocalStorage struct{}

func (browserLocalStorage) Load(key string) (value []byte, found bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("read browser localStorage: %v", recovered)
		}
	}()
	storage := js.Global().Get("localStorage")
	if storage.IsNull() || storage.IsUndefined() {
		return nil, false, fmt.Errorf("browser localStorage is unavailable")
	}
	item := storage.Call("getItem", key)
	if item.IsNull() || item.IsUndefined() {
		return nil, false, nil
	}
	return []byte(item.String()), true, nil
}

func (browserLocalStorage) Store(key string, value []byte) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("write browser localStorage: %v", recovered)
		}
	}()
	storage := js.Global().Get("localStorage")
	if storage.IsNull() || storage.IsUndefined() {
		return fmt.Errorf("browser localStorage is unavailable")
	}
	storage.Call("setItem", key, string(value))
	return nil
}
