package application

import (
	_ "embed"
	"net/http"
)

//go:embed operator.html
var operatorHTML []byte

func operatorPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Write(operatorHTML)
}
