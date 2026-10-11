package launchconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
)

func TestAdminSeparatesBrokerAuthorityFromRuntimeBehavior(t *testing.T) {
	port := uint16(48137)
	unixPath := "/run/override.sock"
	protected := bootstrap.RuntimeProtected{}
	endpoints, err := Admin(json.RawMessage(`{
		"Web":1,"WebFilters":{"UIDs":[7],"Usernames":["alice"]},
		"WebPProf":true,"AllowCrossOrigin":true,
		"Unix":"/run/protected.sock","UnixFilters":{"GIDs":[8]}
	}`), AdminDefaults{}, daemonargs.Args{WebAdmin: &port, UnixAdmin: &unixPath}, &protected)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 2 || endpoints[0].ID != 1 || endpoints[0].Kind != admingate.EndpointWeb || endpoints[0].Address != "127.0.0.1:48137" ||
		len(endpoints[0].Filters.UIDs) != 1 || endpoints[1].ID != 2 || endpoints[1].Address != unixPath || len(endpoints[1].Filters.GIDs) != 1 {
		t.Fatalf("endpoints = %#v", endpoints)
	}
	if protected.Admin == nil || !protected.Admin.WebPProf || !protected.Admin.AllowCrossOrigin {
		t.Fatalf("runtime behavior = %#v", protected.Admin)
	}
	document, err := protected.RuntimeDocument()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"48137", "/run/", "alice", "UIDs", "GIDs"} {
		if strings.Contains(string(document), forbidden) {
			t.Fatalf("runtime document contains broker authority %q: %s", forbidden, document)
		}
	}
}

func TestAdminDefaultsOverridesAndErrors(t *testing.T) {
	for name, test := range map[string]struct {
		raw      string
		defaults AdminDefaults
		args     daemonargs.Args
		wantErr  bool
	}{
		"default unix": {raw: `{}`, defaults: AdminDefaults{Unix: "/run/default.sock"}},
		"unknown":      {raw: `{"Unknown":true}`, defaults: AdminDefaults{Unix: "/run/default.sock"}, wantErr: true},
		"trailing":     {raw: `{} {}`, defaults: AdminDefaults{Unix: "/run/default.sock"}, wantErr: true},
		"disabled":     {raw: `{}`, wantErr: true},
		"nil output":   {raw: `{}`, defaults: AdminDefaults{Unix: "/run/default.sock"}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			var protected *bootstrap.RuntimeProtected
			if name != "nil output" {
				protected = &bootstrap.RuntimeProtected{}
			}
			endpoints, err := Admin(json.RawMessage(test.raw), test.defaults, test.args, protected)
			if (err != nil) != test.wantErr {
				t.Fatalf("endpoints=%#v error=%v", endpoints, err)
			}
		})
	}
}

func FuzzAdmin(f *testing.F) {
	f.Add(`{}`)
	f.Add(`{"Web":8080,"WebFilters":{"UIDs":[1]}}`)
	f.Add(`{"Unix":"/run/admin.sock","WebPProf":true}`)
	f.Fuzz(func(t *testing.T, input string) {
		protected := &bootstrap.RuntimeProtected{}
		endpoints, err := Admin(json.RawMessage(input), AdminDefaults{Unix: "/run/default.sock"}, daemonargs.Args{}, protected)
		if err != nil {
			return
		}
		if len(endpoints) == 0 || len(endpoints) > 3 {
			t.Fatalf("invalid endpoint count: %#v", endpoints)
		}
		if _, err := protected.RuntimeDocument(); err != nil {
			t.Fatalf("invalid runtime document: %v", err)
		}
	})
}
