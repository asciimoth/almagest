package main

import (
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipFileServerServesCompressedAssetsAtOriginalPaths(t *testing.T) {
	static, err := fs.Sub(webStatic, "static")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	response := httptest.NewRecorder()
	gzipFileServer(static).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET /style.css status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", got)
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "body") {
		t.Fatal("decompressed stylesheet does not contain the body rule")
	}
}

func TestEmbeddedPanelContainsOnlyCompressedAssets(t *testing.T) {
	static, err := fs.Sub(webStatic, "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(static, "index.html.gz"); err != nil {
		t.Fatalf("compressed index: %v", err)
	}
	if _, err := fs.Stat(static, "index.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("uncompressed index error = %v, want file not found", err)
	}
}

func TestGzipFileServerServesDocsShellForNestedPagePaths(t *testing.T) {
	static, err := fs.Sub(webStatic, "static")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/docs/guide/welcome", nil)
	response := httptest.NewRecorder()
	gzipFileServer(static).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET nested docs page status = %d, want %d", response.Code, http.StatusOK)
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "Almagest documentation") {
		t.Fatal("nested docs page did not return the documentation shell")
	}
}
