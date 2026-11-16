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

// demoBuild is true only in demo builds. /me then says demo: true, and the
// app skips its Face ID or passcode lock for that practice, so testers can
// use borrowed phones. A practice's Back Office is never a demo build.
var demoBuild = false

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
