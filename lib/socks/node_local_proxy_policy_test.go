package socksnodes

import (
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"

	"github.com/asciimoth/almagest/lib/logging"
)

func TestLocalProxyConfigBackwardCompatibleWithoutRules(t *testing.T) {
	config := defaultLocalProxyConfig()
	config.read(configer.NewMemory(map[string]any{"socksPort": 1180}))
	if config.server.socksPort != 1180 || len(config.exclude) != 0 || len(config.include) != 0 {
		t.Fatalf("read legacy config = %#v", config)
	}
	if err := config.validate(); err != nil {
		t.Fatalf("legacy config validation: %v", err)
	}
	if policy, err := buildLocalProxyClientPolicy(nil, nil, nil); err != nil || policy != nil {
		t.Fatalf("empty policy = %#v, %v; want nil, nil", policy, err)
	}
	if _, err := buildLocalProxyClientPolicy(&sysnetdebug.System{}, []sysnet.Rule{{Type: "uid", Rule: "1"}}, []sysnet.Rule{{Type: "uid", Rule: "2"}}); err == nil {
		t.Fatal("runtime policy accepted simultaneous include and exclude rules")
	}
}

func TestLocalProxyFilteringListenerSkipsRejectedConnections(t *testing.T) {
	system := &sysnetdebug.System{RuleMatcher: func(_ sysnet.Rule, flow sockowner.FlowTuple) (bool, error) {
		return flow.LocalPort == 40001, nil
	}}
	policy, err := buildLocalProxyClientPolicy(system, nil, []sysnet.Rule{{Type: "comm", Rule: "allowed"}})
	if err != nil {
		t.Fatal(err)
	}
	defer policy.Close() //nolint:errcheck
	denied := &trackedLocalProxyConn{socksRouteConn: socksRouteConn{
		local:  &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1080},
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000},
	}}
	allowed := &trackedLocalProxyConn{socksRouteConn: socksRouteConn{
		local:  &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1080},
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40001},
	}}
	listener := filterLocalProxyListener(
		&queuedLocalProxyListener{connections: []net.Conn{denied, allowed}},
		policy,
		logging.NewDebugLogger(nil, logging.DebugLogSource{Name: "test"}),
	)
	got, err := listener.Accept()
	if err != nil || got != allowed {
		t.Fatalf("Accept = %#v, %v; want allowed connection", got, err)
	}
	if !denied.closed || allowed.closed {
		t.Fatalf("connection close state: denied=%v allowed=%v", denied.closed, allowed.closed)
	}
}

func TestLocalProxyConfigRulesRoundTripAndMutualExclusion(t *testing.T) {
	want := []sysnet.Rule{{Type: "comm", Rule: "^curl$"}, {Type: "uid", Rule: "1000"}}
	config := defaultLocalProxyConfig()
	config.read(configer.NewMemory(map[string]any{"exclude": []any{
		map[string]any{"type": " comm ", "rule": " ^curl$ "},
		map[string]string{"type": "uid", "rule": "1000"},
	}}))
	if !reflect.DeepEqual(config.exclude, want) {
		t.Fatalf("exclude rules = %#v, want %#v", config.exclude, want)
	}

	saved := configer.NewMemory(nil)
	if err := saveLocalProxyConfig(saved, config); err != nil {
		t.Fatal(err)
	}
	roundTrip := defaultLocalProxyConfig()
	roundTrip.read(saved)
	if !reflect.DeepEqual(roundTrip.exclude, want) || len(roundTrip.include) != 0 {
		t.Fatalf("round-trip config = %#v", roundTrip)
	}

	roundTrip.include = []sysnet.Rule{{Type: "user", Rule: "alice"}}
	if err := roundTrip.validate(); err == nil {
		t.Fatal("config accepted simultaneous include and exclude rules")
	}
}

func TestLocalProxyClientPolicyIncludeExcludeAndLookupFailure(t *testing.T) {
	lookupFailure := errors.New("lookup failed")
	system := &sysnetdebug.System{RuleMatcher: func(rule sysnet.Rule, _ sockowner.FlowTuple) (bool, error) {
		switch rule.Rule {
		case "match":
			return true, nil
		case "error":
			return false, lookupFailure
		default:
			return false, nil
		}
	}}
	conn := &socksRouteConn{
		local:  &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1080},
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000},
	}

	for _, test := range []struct {
		name    string
		exclude []sysnet.Rule
		include []sysnet.Rule
		want    bool
		wantErr bool
	}{
		{name: "include match", include: []sysnet.Rule{{Type: "comm", Rule: "match"}}, want: true},
		{name: "include miss", include: []sysnet.Rule{{Type: "comm", Rule: "miss"}}},
		{name: "exclude match", exclude: []sysnet.Rule{{Type: "comm", Rule: "match"}}},
		{name: "exclude miss", exclude: []sysnet.Rule{{Type: "comm", Rule: "miss"}}, want: true},
		{name: "lookup failure", include: []sysnet.Rule{{Type: "comm", Rule: "error"}}, wantErr: true},
		{name: "later include match overrides earlier error", include: []sysnet.Rule{{Type: "comm", Rule: "error"}, {Type: "uid", Rule: "match"}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := buildLocalProxyClientPolicy(system, test.exclude, test.include)
			if err != nil {
				t.Fatal(err)
			}
			defer policy.Close() //nolint:errcheck
			got, err := policy.allow(conn)
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("allow = %v, %v; want %v, error=%v", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestLocalProxyMenuAndMatcherContext(t *testing.T) {
	var contexts []sysnet.RuleContext
	system := &sysnetdebug.System{CheckRuleHook: func(_ sysnet.Rule, context sysnet.RuleContext) sysnet.ValidationReport {
		contexts = append(contexts, context.Clone())
		return sysnet.ValidationReport{}
	}}
	node := &localProxyNode{
		NodeBase: NodeBase{Runtime: &testRuntime{system: system}},
		config: localProxyConfig{
			server:  defaultSocksServerConfig(),
			include: []sysnet.Rule{{Type: "comm", Rule: "curl"}},
		},
	}
	if err := node.validateLocalProxyConfig(node.config); err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 || contexts[0].Matcher == nil || contexts[0].Matcher.Family != sysnet.FamilyIPv4 || contexts[0].Matcher.Transport != sysnet.TransportTCP {
		t.Fatalf("validation contexts = %#v", contexts)
	}

	block := localProxyConfigBlock(node.config, "")
	var include, exclude *formular.Item
	for i := range block.Items {
		switch block.Items[i].ID {
		case "include":
			include = &block.Items[i]
		case "exclude":
			exclude = &block.Items[i]
		}
	}
	if include == nil || exclude == nil || include.Field == nil || exclude.Field == nil || len(include.Elements) != 1 {
		t.Fatalf("local proxy rule controls missing from menu: %#v", block)
	}
	if include.Help == "" || exclude.Help == "" {
		t.Fatal("client rule controls do not show the best-effort warning")
	}
}

func TestLocalProxyRuleAutocompleteUsesMatcherCapabilities(t *testing.T) {
	system := &sysnetdebug.System{CompleteRuleHook: func(rule sysnet.Rule, context sysnet.RuleContext) ([]string, error) {
		if context.Matcher == nil || context.Matcher.Transport != sysnet.TransportTCP {
			t.Fatalf("completion context = %#v", context)
		}
		return []string{"curl", "chromium"}, nil
	}}
	node := &localProxyNode{
		NodeBase: NodeBase{Runtime: &testRuntime{system: system}},
		config:   defaultLocalProxyConfig(),
		ruleDraftType: map[string]string{
			"include/new": "exec",
		},
	}
	ref := formular.FieldRef{BlockID: "config", FieldID: "rule", ElementPath: []formular.ElementPathSegment{{ArrayFieldID: "include", ElementID: "new"}}}
	hints := node.localProxyAutocompleteHints(formular.AutocompleteRequestMessage{Field: ref, Prefix: "c"})
	if !reflect.DeepEqual(hints, []string{"curl", "chromium"}) {
		t.Fatalf("completion hints = %#v", hints)
	}
}

type trackedLocalProxyConn struct {
	socksRouteConn
	closed bool
}

func (c *trackedLocalProxyConn) Close() error {
	c.closed = true
	return nil
}

type queuedLocalProxyListener struct {
	connections []net.Conn
}

func (l *queuedLocalProxyListener) Accept() (net.Conn, error) {
	if len(l.connections) == 0 {
		return nil, io.EOF
	}
	conn := l.connections[0]
	l.connections = l.connections[1:]
	return conn, nil
}

func (*queuedLocalProxyListener) Close() error   { return nil }
func (*queuedLocalProxyListener) Addr() net.Addr { return &net.TCPAddr{} }
