//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/asciimoth/gonnect"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/webdemo"
	almagest "github.com/asciimoth/almagest/lib"
)

const requestFunction = "almagestWebDemoRequest"
const eventsFunction = "almagestWebDemoEvents"

func main() {
	backend, err := buildBackend()
	if err != nil {
		panic(err)
	}

	handler := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 {
			return string(backend.Handle([]byte("{}")))
		}
		return string(backend.Handle([]byte(args[0].String())))
	})
	js.Global().Set(requestFunction, handler)
	events := js.FuncOf(func(_ js.Value, _ []js.Value) any { return string(backend.DrainEvents()) })
	js.Global().Set(eventsFunction, events)
	select {}
}

func buildBackend() (*webdemo.Backend, error) {
	persistence := newBrowserLocalStorage()
	stores, err := webdemo.NewStorage(persistence)
	if err != nil {
		return nil, err
	}
	configStore, err := webdemo.NewConfigStore(persistence)
	if err != nil {
		return nil, err
	}
	cfg, err := configStore.Parse()
	if err != nil {
		return nil, err
	}
	logs := webdemo.NewLogFactory(256, browserLog)
	out := gonnect.NewLoopbackNetwork()
	out.AllowAnyHost = true
	local := gonnect.NewLoopbackNetwork()
	local.AllowAnyHost = true
	system := &sysnetdebug.System{OutNetwork: out, LocalNetwork: local}
	engine, err := almagest.BuildEngine(
		context.Background(), cfg, logs,
		system, stores.KV, stores.DNS, stores.Secrets, stores.TLS, nil, nil, false,
	)
	if err != nil {
		return nil, err
	}

	backend, err := webdemo.NewLiveBackend(engine, stores, configStore, logs)
	if err != nil {
		_ = engine.Close()
		return nil, err
	}
	return backend, nil
}

func browserLog(entry api.LogEntry) {
	console := js.Global().Get("console")
	if console.IsNull() || console.IsUndefined() {
		return
	}
	method := entry.Level
	if method == "fatal" {
		method = "error"
	}
	console.Call(method, "[almagest "+entry.Source+"] "+entry.Message)
}
