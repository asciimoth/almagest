// Package launchconfig converts protected bootstrap data into fixed broker
// inputs. It imports no daemon runtime, node, archive, or database code.
package launchconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
)

type AdminDefaults struct {
	Unix string
	Pipe string
}

type adminFilters struct {
	UIDs      []uint32
	GIDs      []uint32
	Usernames []string
}

type adminDocument struct {
	Web              uint16
	WebFilters       adminFilters
	WebPProf         bool
	AllowCrossOrigin bool
	Unix             string
	UnixGroup        string
	UnixFilters      adminFilters
	Pipe             string
}

// Admin converts the protected Admin object and command-line overrides into
// fixed N listeners. It also updates the two R-owned HTTP behavior flags.
// Listener paths, addresses, and peer filters never enter runtimeProtected.
func Admin(raw json.RawMessage, defaults AdminDefaults, arguments daemonargs.Args, runtimeProtected *bootstrap.RuntimeProtected) ([]admingate.EndpointConfig, error) {
	document := adminDocument{Unix: defaults.Unix, Pipe: defaults.Pipe}
	if len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return nil, fmt.Errorf("protected Admin: %w", err)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return nil, fmt.Errorf("protected Admin has trailing data")
		}
	}
	if arguments.WebAdmin != nil {
		document.Web = *arguments.WebAdmin
	}
	if len(arguments.WebAdminFilterUIDs) != 0 {
		document.WebFilters.UIDs = slices.Clone(arguments.WebAdminFilterUIDs)
	}
	if len(arguments.WebAdminFilterGIDs) != 0 {
		document.WebFilters.GIDs = slices.Clone(arguments.WebAdminFilterGIDs)
	}
	if len(arguments.WebAdminFilterUsernames) != 0 {
		document.WebFilters.Usernames = slices.Clone(arguments.WebAdminFilterUsernames)
	}
	if arguments.WebAdminPProf {
		document.WebPProf = true
	}
	if arguments.WebAdminAllowCrossOrigin {
		document.AllowCrossOrigin = true
	}
	if arguments.UnixAdmin != nil {
		document.Unix = *arguments.UnixAdmin
	}
	if len(arguments.UnixAdminFilterUIDs) != 0 {
		document.UnixFilters.UIDs = slices.Clone(arguments.UnixAdminFilterUIDs)
	}
	if len(arguments.UnixAdminFilterGIDs) != 0 {
		document.UnixFilters.GIDs = slices.Clone(arguments.UnixAdminFilterGIDs)
	}
	if len(arguments.UnixAdminFilterUsernames) != 0 {
		document.UnixFilters.Usernames = slices.Clone(arguments.UnixAdminFilterUsernames)
	}
	if arguments.PipeAdmin != nil {
		document.Pipe = *arguments.PipeAdmin
	}
	if runtimeProtected == nil {
		return nil, fmt.Errorf("runtime protected input is nil")
	}
	if document.WebPProf || document.AllowCrossOrigin {
		runtimeProtected.Admin = &bootstrap.RuntimeAdmin{WebPProf: document.WebPProf, AllowCrossOrigin: document.AllowCrossOrigin}
	} else {
		runtimeProtected.Admin = nil
	}
	endpoints := make([]admingate.EndpointConfig, 0, 3)
	if document.Web != 0 {
		endpoints = append(endpoints, admingate.EndpointConfig{ID: 1, Kind: admingate.EndpointWeb,
			Address: net.JoinHostPort("127.0.0.1", fmt.Sprint(document.Web)), Filters: brokerFilters(document.WebFilters)})
	}
	if document.Unix != "" {
		endpoints = append(endpoints, admingate.EndpointConfig{ID: 2, Kind: admingate.EndpointUnix,
			Address: document.Unix, Group: document.UnixGroup, Filters: brokerFilters(document.UnixFilters)})
	}
	if document.Pipe != "" {
		endpoints = append(endpoints, admingate.EndpointConfig{ID: 3, Kind: admingate.EndpointPipe, Address: document.Pipe})
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("isolated daemon requires at least one protected admin endpoint")
	}
	return endpoints, nil
}

func brokerFilters(value adminFilters) admingate.Filters {
	return admingate.Filters{UIDs: slices.Clone(value.UIDs), GIDs: slices.Clone(value.GIDs), Usernames: slices.Clone(value.Usernames)}
}
