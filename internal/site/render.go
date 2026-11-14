package site

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"path"
	"strings"
	"time"

	"github.com/zacharyelston/wehelp/internal/practice"
)

//go:embed templates/*.html templates/style.css
var templates embed.FS

var pages = []struct{ File, Title, Template string }{
	{"index.html", "Home", "index"},
	{"team.html", "Our team", "team"},
	{"services.html", "Services", "services"},
	{"join.html", "Get the app", "join"},
	{"library.html", "Library", "library"},
	{"store.html", "Supply store", "store"},
}

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"price": func(cents int) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) },
	// cap capitalises the first letter, for the practice's words (#108).
	"cap": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	},
	// paras splits text on blank lines; html/template escapes each one.
	"paras": func(s string) []string {
		var out []string
		for _, p := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	},
	"lines": func(s string) []string {
		var out []string
		for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	},
	"filesize": func(n int) string {
		if n >= 1<<20 {
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		}
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	},
}).ParseFS(templates, "templates/*.html"))

type libraryFile struct {
	Title, Href, Kind string
	Size              int
}

type category struct {
	Name     string
	Products []Product
}

type pageData struct {
	Content
	Page       string
	Title      string
	Nav        []struct{ File, Title string }
	Downloads  []libraryFile
	Categories []category
	Images     map[string]bool
	Updated    string
}

// Render returns every file of the site by path: the pages, the stylesheet
// and files/<filename> for each upload.
func Render(c Content, products []Product, files []File, now time.Time) (map[string][]byte, error) {
	if c.PracticeName == "" {
		c.PracticeName = "Our practice"
	}
	if c.Words.Patient == "" {
		c.Words = practice.Default.Words
	}
	out := map[string][]byte{}
	css, err := templates.ReadFile("templates/style.css")
	if err != nil {
		return nil, err
	}
	out["style.css"] = css

	d := pageData{Content: c, Images: map[string]bool{}, Updated: now.Format("January 2, 2006")}
	for _, p := range pages {
		d.Nav = append(d.Nav, struct{ File, Title string }{p.File, p.Title})
	}
	for _, f := range files {
		if _, ok := AllowedTypes[path.Ext(f.Filename)]; !ok || !filenameRE.MatchString(f.Filename) {
			return nil, fmt.Errorf("file %q has a type the site can't serve", f.Filename)
		}
		out["files/"+f.Filename] = f.Data
		if f.Kind == "image" {
			d.Images[f.Filename] = true
		} else {
			d.Downloads = append(d.Downloads, libraryFile{Title: f.Title, Href: "files/" + f.Filename,
				Kind: strings.ToUpper(strings.TrimPrefix(path.Ext(f.Filename), ".")), Size: len(f.Data)})
		}
	}
	// Photos and the logo must be uploaded images; drop dangling names.
	if !d.Images[c.LogoFile] {
		d.LogoFile = ""
	}
	d.Providers = append([]Provider(nil), c.Providers...)
	for i := range d.Providers {
		if !d.Images[d.Providers[i].PhotoFile] {
			d.Providers[i].PhotoFile = ""
		}
	}
	for _, p := range products {
		name := p.Category
		if name == "" {
			name = "Supplies"
		}
		if n := len(d.Categories); n > 0 && d.Categories[n-1].Name == name {
			d.Categories[n-1].Products = append(d.Categories[n-1].Products, p)
		} else {
			d.Categories = append(d.Categories, category{Name: name, Products: []Product{p}})
		}
	}

	for _, p := range pages {
		d.Page, d.Title = p.File, p.Title
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, p.Template, d); err != nil {
			return nil, fmt.Errorf("render %s: %w", p.File, err)
		}
		out[p.File] = buf.Bytes()
	}
	return out, nil
}
