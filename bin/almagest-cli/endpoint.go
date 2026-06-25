package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

const defaultEndpoint = "unix://run/almagest/admin.sock"

type endpointKind string

const (
	endpointUnix endpointKind = "unix"
	endpointWeb  endpointKind = "web"
)

// endpoint is the normalized form of an admin API location.
type endpoint struct {
	kind endpointKind
	path string
	url  string
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
	default:
		if strings.TrimSpace(u.Scheme) == "" {
			return endpoint{}, fmt.Errorf("endpoint URL must use unix:// or web://")
		}
		return endpoint{}, fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}
}
