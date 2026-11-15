package server

import (
	"net/http"

	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// demoLoginsJS is served as /demo.js. It is empty in field builds; demo
// builds (build tag demo, demo_logins.go) set it to the demo logins, which
// the sign-in page then lists (#161). Demo hosts are for showing WeHelp, with
// fixed logins anyone can use; a practice's Back Office never has them.
var demoLoginsJS = "// Not a demo build: no demo logins.\n"

func serveDemoJS(w http.ResponseWriter, r *http.Request) {
	if tunnel.ViaRelay(r.Context()) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	h := w.Header()
	for k, v := range staffWebHeaders {
		h.Set(k, v)
	}
	h.Set("content-type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(demoLoginsJS))
}
