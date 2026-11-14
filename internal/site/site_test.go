package site

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/practice"
)

func TestRenderEscapesAndHasNoScripts(t *testing.T) {
	c := DemoContent(`Shaw & Co <script>alert(1)</script>`)
	c.About = "Line one.\n\n<script>evil()</script> and <b>bold</b>"
	c.LogoFile = "logo.png"
	c.Providers[0].PhotoFile = "missing.png" // not uploaded: dropped
	files := []File{
		{Kind: "image", Title: "Logo", Filename: "logo.png", Data: []byte("png")},
		{Kind: "library", Title: "Sleep guide", Filename: "sleep-guide.pdf", Data: []byte("%PDF-1.4")},
	}
	out, err := Render(c, DemoProducts(), files, time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"index.html", "team.html", "services.html", "join.html", "library.html", "store.html",
		"style.css", "files/logo.png", "files/sleep-guide.pdf"} {
		if out[want] == nil {
			t.Errorf("missing %s", want)
		}
	}
	for name, data := range out {
		if strings.HasSuffix(name, ".html") && bytes.Contains(bytes.ToLower(data), []byte("<script")) {
			t.Errorf("%s contains a script tag", name)
		}
	}
	idx := string(out["index.html"])
	if !strings.Contains(idx, "&lt;script&gt;evil()") || !strings.Contains(idx, `src="files/logo.png"`) {
		t.Errorf("index not escaped or logo missing:\n%s", idx)
	}
	if strings.Contains(string(out["team.html"]), "missing.png") {
		t.Error("photo that was never uploaded is linked")
	}
	store := string(out["store.html"])
	if !strings.Contains(store, "Order in the WeHelp app") || !strings.Contains(store, "$28.00") || strings.Contains(store, "<form") {
		t.Errorf("store page:\n%s", store)
	}
	if !strings.Contains(string(out["library.html"]), `href="files/sleep-guide.pdf"`) {
		t.Error("library download missing")
	}
	if !strings.Contains(string(out["join.html"]), "Scan your pairing code") {
		t.Error("join page lacks the pairing steps")
	}
}

func TestBundleIsATar(t *testing.T) {
	b, n, err := Bundle(DemoContent("Demo"), DemoProducts(), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(b))
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, "..") || strings.HasPrefix(h.Name, "/") {
			t.Errorf("bad name %s", h.Name)
		}
		count++
	}
	if count != n || n != 7 {
		t.Errorf("files %d, reported %d", count, n)
	}
}

func TestValidate(t *testing.T) {
	ok := DemoContent("Demo")
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Content){
		"http podcast":       func(c *Content) { c.Podcasts[0].URL = "http://example.com" },
		"javascript podcast": func(c *Content) { c.Podcasts[0].URL = "javascript:alert(1)" },
		"app link":           func(c *Content) { c.AppURL = "ftp://x" },
		"unnamed provider":   func(c *Content) { c.Providers[0].Name = "" },
		"long about":         func(c *Content) { c.About = strings.Repeat("a", 5001) },
	} {
		c := DemoContent("Demo")
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCleanFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Sleep Guide.PDF":     "sleep-guide.pdf",
		"../../etc/photo.jpg": "photo.jpg",
		`C:\Users\a\logo.png`: "logo.png",
	} {
		if got, err := CleanFilename(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"page.html", "x.css", "run.sh", "image.svg", "...", ".pdf"} {
		if got, err := CleanFilename(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

// TestThemes: every theme renders as data-theme with its CSS; an unknown
// theme is refused, and an empty one renders the default (#104).
func TestThemes(t *testing.T) {
	css, _ := templates.ReadFile("templates/style.css")
	for i, th := range Themes {
		c := DemoContent("Clinic")
		c.Theme = th.ID
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", th.ID, err)
		}
		out, err := Render(c, nil, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out["index.html"]), `data-theme="`+th.ID+`"`) {
			t.Errorf("%s: not on <html>", th.ID)
		}
		if i > 0 && !strings.Contains(string(css), `[data-theme="`+th.ID+`"]`) {
			t.Errorf("%s: no CSS", th.ID)
		}
	}
	c := DemoContent("Clinic")
	out, _ := Render(c, nil, nil, time.Now())
	if !strings.Contains(string(out["index.html"]), `data-theme="`+Themes[0].ID+`"`) {
		t.Error("empty theme does not render the default")
	}
	c.Theme = `x" onload="alert(1)`
	if c.Validate() == nil {
		t.Error("unknown theme accepted")
	}
}

// TestWords: the practice type's words reach the pages (#108); without
// any, the default words do.
func TestWords(t *testing.T) {
	c := DemoContent("Shear Joy Salon")
	c.Words = practice.Words{Patient: "client", Patients: "clients", Provider: "stylist", Providers: "stylists"}
	out, err := Render(c, nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	idx, join := string(out["index.html"]), string(out["join.html"])
	if !strings.Contains(idx, "Clients use the free WeHelp app") || !strings.Contains(join, "add you as a client") || strings.Contains(idx+join, "atient") {
		t.Error("salon words not used")
	}
	out, _ = Render(DemoContent("Clinic"), nil, nil, time.Now())
	if !strings.Contains(string(out["index.html"]), "Patients use the free WeHelp app") {
		t.Error("default words not used")
	}
}

// TestBlog: posts get a Blog page, a page each, and the home-page teaser;
// text is escaped; a missing image is dropped; no posts, no Blog (#121).
func TestBlog(t *testing.T) {
	c := DemoContent("Clinic")
	day := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	c.Posts = []Post{
		{ID: "11111111-aaaa", Title: "Flu shots are here!", Body: "First paragraph.\n\nSecond <script>x()</script>.", ImageFile: "flu.png", Author: "Dr. Ada Shaw", Date: day},
		{ID: "22222222-bbbb", Title: "Holiday hours", Body: "Closed Dec 25.", ImageFile: "missing.png", Author: "Front Desk", Date: day.Add(-24 * time.Hour)},
	}
	files := []File{{Kind: "image", Title: "Flu", Filename: "flu.png", Data: []byte("png")}}
	out, err := Render(c, nil, files, day)
	if err != nil {
		t.Fatal(err)
	}
	flu, holiday := c.Posts[0].Slug(), c.Posts[1].Slug()
	if flu != "post-2026-11-14-flu-shots-are-here-11111111.html" {
		t.Errorf("slug %q", flu)
	}
	for _, want := range []string{"blog.html", flu, holiday} {
		if out[want] == nil {
			t.Fatalf("missing %s", want)
		}
	}
	idx, blog, post := string(out["index.html"]), string(out["blog.html"]), string(out[flu])
	if !strings.Contains(idx, "Latest news") || !strings.Contains(idx, `href="`+flu+`"`) || !strings.Contains(idx, `href="blog.html"`) {
		t.Error("home page lacks the latest posts or the Blog link")
	}
	if strings.Index(blog, "Flu shots") > strings.Index(blog, "Holiday hours") {
		t.Error("blog not newest first")
	}
	if !strings.Contains(post, "&lt;script&gt;") || strings.Contains(post, "<script") || !strings.Contains(post, `src="files/flu.png"`) {
		t.Error("post not escaped or image missing")
	}
	if strings.Contains(string(out[holiday]), "missing.png") {
		t.Error("a dangling image name was kept")
	}
	c.Posts = nil
	out, _ = Render(c, nil, nil, day)
	if out["blog.html"] != nil || strings.Contains(string(out["index.html"]), "blog.html") {
		t.Error("a site with no posts has a Blog")
	}
}
