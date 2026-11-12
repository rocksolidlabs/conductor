// Package web serves the generated Nuxt single-page app embedded at build time.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:generate make -C ../.. web-build

//go:embed all:dist
var dist embed.FS

// Available reports whether a generated index.html is embedded.
func Available() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}

// PreviewBridge is the preview bridge the web build emits
// (preview-bridge.js, web/build/previewBridge.ts), nil when the embedded app has
// none (a server built without make web-build).
func PreviewBridge() []byte {
	b, err := fs.ReadFile(dist, "dist/preview-bridge.js")
	if err != nil {
		return nil
	}
	return b
}

// Handler serves the SPA: static assets by path, index.html for every other
// route so client-side routing works on reload. It returns nil when the UI
// has not been built.
func Handler() http.Handler {
	if !Available() {
		return nil
	}
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return handler(sub)
}

// handler is Handler for the generated app in fsys.
func handler(fsys fs.FS) http.Handler {
	files := http.FS(fsys)
	return app{files: files, static: http.FileServer(files)}
}

// app is the generated app: it serves it, and names its files (Holds).
type app struct {
	files  http.FileSystem
	static http.Handler
}

func (a app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if p := path.Clean("/" + r.URL.Path); a.isFile(p) {
		if strings.HasPrefix(p, "/_nuxt/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		a.static.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	a.static.ServeHTTP(w, r2)
}

// Holds reports whether p is, exactly as spelt, the path of one of the
// app's files: the request log names such a request by its path, the
// server's own text (internal/api, logPath).
func (a app) Holds(p string) bool {
	return p != "" && p[0] == '/' && path.Clean(p) == p && a.isFile(p)
}

func (a app) isFile(p string) bool {
	f, err := a.files.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && !st.IsDir()
}
