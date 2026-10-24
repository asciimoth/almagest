package socksnodes

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

type localProxyRuleMatcher struct {
	rule    sysnet.Rule
	matcher sysnet.Matcher
}

type localProxyClientPolicy struct {
	mu      sync.Mutex
	include bool
	rules   []localProxyRuleMatcher
}

func buildLocalProxyClientPolicy(system sysnet.System, exclude, include []sysnet.Rule) (*localProxyClientPolicy, error) {
	if len(exclude) != 0 && len(include) != 0 {
		return nil, errors.New("exclude and include client rules are mutually exclusive")
	}
	rules := exclude
	includeMode := false
	if len(include) != 0 {
		rules = include
		includeMode = true
	}
	if len(rules) == 0 {
		return nil, nil
	}
	if system == nil {
		return nil, errors.New("system network is unavailable")
	}
	policy := &localProxyClientPolicy{include: includeMode, rules: make([]localProxyRuleMatcher, 0, len(rules))}
	for _, rule := range rules {
		matcher, err := system.BuildMatcher(rule)
		if err != nil {
			_ = policy.Close()
			return nil, fmt.Errorf("%s rule %q: %w", rule.Type, rule.Rule, err)
		}
		if matcher == nil {
			_ = policy.Close()
			return nil, fmt.Errorf("%s rule %q: matcher is unavailable", rule.Type, rule.Rule)
		}
		policy.rules = append(policy.rules, localProxyRuleMatcher{rule: rule, matcher: matcher})
	}
	return policy, nil
}

func (p *localProxyClientPolicy) allow(conn net.Conn) (bool, error) {
	if p == nil {
		return true, nil
	}
	// Matcher implementations can use shared native tracking state. Serialize
	// admission and Close so a worker restart cannot close an active lookup.
	p.mu.Lock()
	defer p.mu.Unlock()
	var lookupErr error
	for _, entry := range p.rules {
		matched, err := sysnet.MatchConn(conn, entry.matcher)
		if err != nil {
			lookupErr = errors.Join(lookupErr, fmt.Errorf("%s rule %q: %w", entry.rule.Type, entry.rule.Rule, err))
			continue
		}
		if matched {
			return p.include, nil
		}
	}
	if lookupErr != nil {
		return false, lookupErr
	}
	return !p.include, nil
}

func (p *localProxyClientPolicy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var err error
	for _, entry := range p.rules {
		err = errors.Join(err, entry.matcher.Close())
	}
	p.rules = nil
	return err
}

type localProxyFilteringListener struct {
	net.Listener
	policy *localProxyClientPolicy
	logger pasta.Logger
}

func filterLocalProxyListener(listener net.Listener, policy *localProxyClientPolicy, logger pasta.Logger) net.Listener {
	if listener == nil || policy == nil {
		return listener
	}
	return &localProxyFilteringListener{Listener: listener, policy: policy, logger: logger}
}

func (l *localProxyFilteringListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		allowed, matchErr := l.policy.allow(conn)
		if allowed {
			return conn, nil
		}
		remote := conn.RemoteAddr()
		_ = conn.Close()
		if l.logger == nil {
			continue
		}
		if matchErr != nil {
			l.logger.Debugf("local proxy client %s rejected after ownership lookup failed: %v", remote, matchErr)
		} else {
			l.logger.Debugf("local proxy client %s rejected by client rules", remote)
		}
	}
}
