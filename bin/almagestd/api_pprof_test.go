package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterPProfHandlers(t *testing.T) {
	mux := http.NewServeMux()
	registerPProfHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "goroutine") {
		t.Fatalf("GET /debug/pprof/ body does not contain profile index: %q", body)
	}
}
