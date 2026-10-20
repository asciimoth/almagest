package main

import (
	"bytes"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// gzipFileServer exposes a gzip-only embedded tree under its original asset
// names. The web admin supports browsers, which accept gzip content encoding.
func gzipFileServer(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "docs" || strings.HasPrefix(name, "docs/") {
			name = "docs/index.html"
		} else if name == "." || strings.HasSuffix(r.URL.Path, "/") {
			name = path.Join(name, "index.html")
		}
		contents, err := fs.ReadFile(files, name+".gz")
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Encoding", "gzip")
		http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(contents))
	})
}
