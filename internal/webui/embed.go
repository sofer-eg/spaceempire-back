// Package webui embeds the built React SPA so the production binary serves
// the frontend itself (no separate static host). `make release` overwrites
// dist/ with the real `front` build before `go build`; the committed
// placeholder index.html keeps `go build` / `make run` working in dev (where
// the SPA is actually served by the Vite dev server on :5173). Phase 7.3.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the embedded SPA: a real file under dist/ is served as-is;
// any other path falls back to index.html so client-side routing (React
// Router: /sector, /clans, /bounties, …) works on a hard reload.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// dist is embedded at compile time — Sub cannot fail at runtime.
		panic("webui: embed dist: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	index, _ := fs.ReadFile(sub, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := sub.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		if index == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
