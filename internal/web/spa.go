package web

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"
)

// The built React/Vite SPA, embedded so the binary stays self-contained. Built
// with `npm --prefix internal/web/spa run build`; Vite uses base "/app/", so all
// asset URLs resolve under the mount below.
//
//go:embed all:spa/dist
var spaAssets embed.FS

// spaHandler serves the SPA under /app: hashed assets straight from dist, and
// index.html as the fallback for any other path (client-side routing).
func (s *Server) spaHandler() http.Handler {
	sub, err := fs.Sub(spaAssets, "spa/dist")
	if err != nil {
		log.Fatalf("spa: sub: %v", err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		log.Fatalf("spa: index.html missing (run the SPA build): %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))

	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/app"), "/")
		if p == "" || p == "index.html" {
			serveIndex(w)
			return
		}
		f, err := sub.Open(p)
		if err != nil {
			// Unknown path → let the SPA's client router handle it.
			serveIndex(w)
			return
		}
		// A directory (e.g. /app/assets) would otherwise render an index listing;
		// fall through to the SPA shell instead of exposing the file tree.
		if st, e := f.Stat(); e == nil && st.IsDir() {
			_ = f.Close()
			serveIndex(w)
			return
		}
		_ = f.Close()
		// Vite emits content-hashed asset filenames, so they're safe to cache hard.
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		r2 := new(http.Request)
		*r2 = *r
		r2.URL.Path = "/" + p
		fileServer.ServeHTTP(w, r2)
	})
}
