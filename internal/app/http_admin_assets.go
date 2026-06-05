package app

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed admin_dist/*
var adminDist embed.FS

func (h *HTTPHandler) adminAppPage(w http.ResponseWriter, r *http.Request) {
	serveAdminFile(w, r, "index.html", false)
}

func (h *HTTPHandler) adminAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/")
	if name == "" || strings.HasSuffix(name, "/") {
		name = "index.html"
	}
	serveAdminFile(w, r, name, strings.HasPrefix(name, "assets/"))
}

func serveAdminFile(w http.ResponseWriter, r *http.Request, name string, cacheable bool) {
	adminFiles, err := fs.Sub(adminDist, "admin_dist")
	if err != nil {
		http.Error(w, "admin assets are unavailable", http.StatusInternalServerError)
		return
	}
	if _, err := fs.Stat(adminFiles, name); err != nil {
		http.NotFound(w, r)
		return
	}
	if cacheable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	http.ServeFileFS(w, r, adminFiles, name)
}
