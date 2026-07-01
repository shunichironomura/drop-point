package httpapi

import (
	"net/http"

	droppage "github.com/shunichironomura/drop-point/web/drop-page"
)

const dropPageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// HandleServeDropPage serves the sender-facing browser encryption page.
func HandleServeDropPage(w http.ResponseWriter, r *http.Request) {
	setDropPageSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data, err := droppage.Files.ReadFile("index.html")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

// HandleDropPageAsset serves same-origin static assets for the drop page.
func HandleDropPageAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("asset")
	if name != "styles.css" && name != "app.js" {
		http.NotFound(w, r)
		return
	}
	setDropPageSecurityHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	switch name {
	case "styles.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "app.js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	data, err := droppage.Files.ReadFile(name)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

func setDropPageSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", dropPageCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
}
