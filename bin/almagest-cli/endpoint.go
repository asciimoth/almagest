package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

type endpointKind string

const (
	endpointUnix endpointKind = "unix"
	endpointPipe endpointKind = "pipe"
	endpointWeb  endpointKind = "web"
)

type pipeTrust string

const (
	pipeTrustUser    pipeTrust = "user"
	pipeTrustMachine pipeTrust = "machine"
)

// endpoint is the normalized form of an admin API location.
type endpoint struct {
	kind      endpointKind
	path      string
	url       string
	pipeTrust pipeTrust
	pipeName  string
}

// parseEndpoint accepts the CLI endpoint URL syntax. Unix socket URLs are
// normalized to absolute filesystem paths; relative socket paths are rejected.
func parseEndpoint(raw string) (endpoint, error) {
	if raw == "" {
		raw = defaultEndpoint
	}
	u, err := url.Parse(raw)
	if err != nil {
		return endpoint{}, fmt.Errorf("parse endpoint: %w", err)
	}
	switch u.Scheme {
	case "unix":
		path := u.Path
		if u.Host != "" {
			path = "/" + u.Host + u.Path
		}
		if path == "" && u.Opaque != "" {
			path = u.Opaque
		}
		if path == "" {
			return endpoint{}, fmt.Errorf("unix endpoint path is empty")
		}
		if !filepath.IsAbs(path) {
			return endpoint{}, fmt.Errorf("unix endpoint path must be absolute: %s", path)
		}
		return endpoint{kind: endpointUnix, path: filepath.Clean(path)}, nil
	case "web":
		if u.Host == "" {
			return endpoint{}, fmt.Errorf("web endpoint host is empty")
		}
		path := u.Path
		if path == "" || path == "/" {
			path = "/api/ws"
		}
		ws := url.URL{
			Scheme:   "ws",
			Host:     u.Host,
			Path:     path,
			RawQuery: u.RawQuery,
		}
		return endpoint{kind: endpointWeb, url: ws.String()}, nil
	case "pipe":
		if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return endpoint{}, fmt.Errorf("pipe endpoint must not contain credentials, a query, or a fragment")
		}
		trust := pipeTrust(u.Host)
		if trust != pipeTrustUser && trust != pipeTrustMachine {
			return endpoint{}, fmt.Errorf("pipe endpoint host must be %q or %q", pipeTrustUser, pipeTrustMachine)
		}
		name := strings.TrimPrefix(u.EscapedPath(), "/")
		if name != "" {
			decoded, err := url.PathUnescape(name)
			if err != nil {
				return endpoint{}, fmt.Errorf("pipe endpoint name: %w", err)
			}
			if decoded == "." || decoded == ".." || strings.ContainsAny(decoded, `/\\`) {
				return endpoint{}, fmt.Errorf("pipe endpoint name must be one local pipe component")
			}
			name = decoded
		}
		return endpoint{kind: endpointPipe, pipeTrust: trust, pipeName: name}, nil
	default:
		if strings.TrimSpace(u.Scheme) == "" {
			return endpoint{}, fmt.Errorf("endpoint URL must use unix://, pipe://, or web://")
		}
		return endpoint{}, fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}
}
