package main

import (
	"context"
	"net"
	"strconv"
	"sync"

	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/kataras/golog"
)

type AdminAPI struct {
	webListener net.Listener
}

func (a *AdminAPI) Stop() {
	if a.webListener != nil {
		golog.Debug("Closing web admin listener")
		_ = a.webListener.Close()
	}
}

func (a *AdminAPI) Run(
	ctx context.Context,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	engine lib.Engine,
	sys sysnet.SysBuilder,
) {
	if a.webListener != nil { //nolint
		// TODO: Run http+websocket server over wg
	}
}

func PrepareAdminAPI(cfg *Config) (*AdminAPI, error) {
	api := AdminAPI{}
	if cfg.Protected.Admin.Web != 0 {
		listener, err := net.Listen(
			"tcp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(int(cfg.Protected.Admin.Web))),
		)
		if err != nil {
			return nil, err
		}
		api.webListener = listener
	}
	return &api, nil
}
