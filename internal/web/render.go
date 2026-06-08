package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
)

//go:embed templates static
var assets embed.FS

type renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
}

var funcMap = template.FuncMap{
	"inc":  func(i int) int { return i + 1 },
	"dec":  func(i int) int { return i - 1 },
	"add":  func(a, b int) int { return a + b },
	"urlq": url.QueryEscape,
}

func newRenderer() *renderer {
	partials, err := template.New("partials").Funcs(funcMap).ParseFS(assets, "templates/partials/*.html")
	if err != nil {
		log.Fatalf("render: parse partials: %v", err)
	}
	pageFiles, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		log.Fatalf("render: glob pages: %v", err)
	}
	r := &renderer{pages: map[string]*template.Template{}, partials: partials}
	for _, pf := range pageFiles {
		name := baseName(pf)
		t := template.New("base.html").Funcs(funcMap)
		t = template.Must(t.ParseFS(assets, "templates/base.html"))
		t = template.Must(t.ParseFS(assets, "templates/partials/*.html"))
		t = template.Must(t.ParseFS(assets, pf))
		r.pages[name] = t
	}
	return r
}

// page renders a full page (base layout + named page content).
func (r *renderer) page(w http.ResponseWriter, name string, data any) {
	t, ok := r.pages[name]
	if !ok {
		http.Error(w, "unknown page: "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base.html", data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// partial renders a single named fragment (for HTMX swaps).
func (r *renderer) partial(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := r.partials.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			p = p[i+1:]
			break
		}
	}
	if len(p) > 5 && p[len(p)-5:] == ".html" {
		p = p[:len(p)-5]
	}
	return p
}

func staticFS() http.Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatalf("render: static sub: %v", err)
	}
	// embed.FS reports a zero ModTime, so http.FileServer emits no
	// Last-Modified/ETag and browsers fall back to heuristic caching — which
	// serves stale JS/CSS across deploys (e.g. a cached workbench.js missing a
	// newly added global). Precompute a content-hash ETag so there's a real
	// validator, and send Cache-Control: no-cache so the browser revalidates on
	// every load and picks up a changed asset immediately.
	etags := map[string]string{}
	_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := fs.ReadFile(sub, p)
		if rerr != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		etags["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if et, ok := etags[strings.TrimPrefix(r.URL.Path, "/static")]; ok {
			w.Header().Set("ETag", et)
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}
