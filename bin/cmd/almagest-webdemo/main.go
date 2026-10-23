//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/webdemo"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
)

const requestFunction = "almagestWebDemoRequest"

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
	select {}
}

func buildBackend() (*webdemo.Backend, error) {
	out := gonnect.NewLoopbackNetwork()
	out.AllowAnyHost = true
	local := gonnect.NewLoopbackNetwork()
	local.AllowAnyHost = true
	system := &sysnetdebug.System{OutNetwork: out, LocalNetwork: local}
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1),
		system, nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		return nil, err
	}

	snapshot, ok := engine.WorkspaceSnapshot(almagest.MainWorkspace)
	if !ok {
		_ = engine.Close()
		return nil, almagest.ErrNoWorkspace
	}
	publicSnapshot := api.NewWorkspaceSnapshot(snapshot, nil)
	if err := engine.Close(); err != nil {
		return nil, err
	}
	return webdemo.NewBackend(publicSnapshot), nil
}
