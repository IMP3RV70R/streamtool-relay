package application

import (
	"net/http"
	"os"
	"path/filepath"
)

func (a *API) webHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "index.html"
		switch r.URL.Path {
		case "/", "/dashboard":
		case "/app.js":
			name = "app.js"
		case "/style.css":
			name = "style.css"
		default:
			http.NotFound(w, r)
			return
		}
		root := a.WebDir
		if root == "" {
			root = "../apps/web"
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			http.Error(w, "Web client unavailable", 503)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		types := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}
		w.Header().Set("Content-Type", types[name])
		w.Write(data)
	})
}
