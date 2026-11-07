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
	static := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if f, err := files.Open(p); err == nil {
			st, statErr := f.Stat()
			f.Close()
			if statErr == nil && !st.IsDir() {
				if strings.HasPrefix(p, "/_nuxt/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				static.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		static.ServeHTTP(w, r2)
	})
}
