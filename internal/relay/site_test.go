package relay

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"golang.org/x/crypto/ssh"
)

type tarFile struct {
	name string
	data string
	typ  byte
}

func makeTar(t *testing.T, files ...tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		typ := f.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), Typeflag: typ}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f.data))
	}
	_ = tw.Close()
	return buf.Bytes()
}

func TestReadSiteBundle(t *testing.T) {
	index := tarFile{name: "index.html", data: "<h1>Hi</h1>"}
	good := makeTar(t, index, tarFile{name: "style.css", data: "body{}"}, tarFile{name: "files/guide.pdf", data: "%PDF"})
	files, err := ReadSiteBundle(bytes.NewReader(good))
	if err != nil || len(files) != 3 || string(files["files/guide.pdf"]) != "%PDF" {
		t.Fatalf("good bundle: %v %v", files, err)
	}
	bad := map[string][]byte{
		"script":       makeTar(t, tarFile{name: "index.html", data: "<p>x</p><SCRIPT>alert(1)</script>"}),
		"no index":     makeTar(t, tarFile{name: "about.html", data: "x"}),
		"traversal":    makeTar(t, index, tarFile{name: "../escape.html", data: "x"}),
		"absolute":     makeTar(t, index, tarFile{name: "/etc/passwd.txt", data: "x"}),
		"deep":         makeTar(t, index, tarFile{name: "a/b/c.png", data: "x"}),
		"upper case":   makeTar(t, index, tarFile{name: "Files/X.PDF", data: "x"}),
		"svg":          makeTar(t, index, tarFile{name: "files/logo.svg", data: "<svg/>"}),
		"javascript":   makeTar(t, index, tarFile{name: "app.js", data: "x"}),
		"symlink":      makeTar(t, index, tarFile{name: "files/link.pdf", typ: tar.TypeSymlink}),
		"duplicate":    makeTar(t, index, index),
		"not a tar":    []byte("hello, this is not a tar file at all, but long enough to read a header from it maybe"),
		"too big file": makeTar(t, index, tarFile{name: "files/big.pdf", data: strings.Repeat("x", MaxSiteFileBytes+1)}),
	}
	for name, b := range bad {
		if _, err := ReadSiteBundle(bytes.NewReader(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var many []tarFile
	many = append(many, index)
	for i := 0; i < MaxSiteFiles; i++ {
		many = append(many, tarFile{name: "files/" + uuid.NewString() + ".txt", data: "x"})
	}
	if _, err := ReadSiteBundle(bytes.NewReader(makeTar(t, many...))); err == nil {
		t.Error("too many files: accepted")
	}
}

func testRelay(t *testing.T, officeIDs ...string) *Relay {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hostPriv)
	regPub, _, _ := ed25519.GenerateKey(rand.Reader)
	r, err := New(Config{RelayID: "relay-1", HostKey: signer, RegistryKey: regPub, SitesDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	l := officelist.List{Version: 1}
	for _, id := range officeIDs {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		sp, _ := ssh.NewPublicKey(pub)
		l.Offices = append(l.Offices, officelist.Office{OfficeID: id, RelayID: "relay-1", SSHPublicKey: string(ssh.MarshalAuthorizedKey(sp))})
	}
	r.apply(l)
	return r
}

func TestSiteServing(t *testing.T) {
	office, other := uuid.NewString(), uuid.NewString()
	r := testRelay(t, office, other)
	files, err := ReadSiteBundle(bytes.NewReader(makeTar(t,
		tarFile{name: "index.html", data: "<h1>Practice</h1>"},
		tarFile{name: "style.css", data: "body{}"},
		tarFile{name: "files/guide.pdf", data: "%PDF-1.4"},
		tarFile{name: "files/logo.png", data: "png"})))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.storeSite(office, files); err != nil {
		t.Fatal(err)
	}
	// Republishing swaps the whole site.
	if _, err := r.storeSite(office, files); err != nil {
		t.Fatal(err)
	}
	h := r.SiteHandler()
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	rec := get("GET", "/s/"+office+"/")
	if rec.Code != 200 || rec.Body.String() != "<h1>Practice</h1>" {
		t.Fatalf("index: %d %q", rec.Code, rec.Body.String())
	}
	for k, want := range map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Content-Security-Policy": siteCSP,
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q", k, got)
		}
	}
	if rec := get("GET", "/s/"+office+"/files/guide.pdf"); rec.Code != 200 || rec.Header().Get("Content-Type") != "application/pdf" ||
		rec.Header().Get("Content-Disposition") != "attachment" {
		t.Errorf("pdf: %d %v", rec.Code, rec.Header())
	}
	if rec := get("GET", "/s/"+office+"/files/logo.png"); rec.Code != 200 || rec.Header().Get("Content-Disposition") != "" {
		t.Errorf("image: %d %v", rec.Code, rec.Header())
	}
	if rec := get("GET", "/s/"+office); rec.Code != http.StatusMovedPermanently {
		t.Errorf("no trailing slash: %d", rec.Code)
	}
	if rec := get("POST", "/s/"+office+"/"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
	for _, p := range []string{
		"/s/" + other + "/",                    // listed, nothing published
		"/s/" + uuid.NewString() + "/",         // not listed
		"/s/" + office + "/../" + other + "/",  // traversal
		"/s/" + office + "/files/../../x.html", // traversal
		"/s/" + office + "/missing.html",
		"/s/" + office + "/files",
		"/s/" + strings.ToUpper(office) + "/",
		"/",
		"/s/",
	} {
		if rec := get("GET", p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}

	// An office dropped from the list loses its site at once.
	r.apply(officelist.List{Version: 2})
	if rec := get("GET", "/s/"+office+"/"); rec.Code != http.StatusNotFound {
		t.Errorf("unlisted office still served: %d", rec.Code)
	}
}
