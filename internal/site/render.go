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

type page struct{ File, Title, Template string }

var pages = []page{
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

// postView is a post as the templates use it.
type postView struct {
	Post
	File string
	Day  string
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
	Posts      []postView // the blog (#121)
	Latest     []postView // the newest three, for the home page
	Post       postView   // on a post's own page
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
	var shown []page
	for _, p := range pages {
		if (p.File == "store.html" && c.Off["store"]) || (p.File == "library.html" && c.Off["library"]) {
			continue
		}
		shown = append(shown, p)
		// The blog comes after the team page, and only when there are posts (#121).
		if p.File == "team.html" && len(c.Posts) > 0 {
			shown = append(shown, page{"blog.html", "Blog", "blog"})
		}
	}
	for _, p := range shown {
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

	// Blog posts (#121): newest first, at most MaxPosts; a dangling image
	// name is dropped like a provider photo's.
	posts := c.Posts
	if len(posts) > MaxPosts {
		posts = posts[:MaxPosts]
	}
	for _, p := range posts {
		if !d.Images[p.ImageFile] {
			p.ImageFile = ""
		}
		d.Posts = append(d.Posts, postView{Post: p, File: p.Slug(), Day: p.Date.In(now.Location()).Format("January 2, 2006")})
	}
	if len(d.Posts) > 3 {
		d.Latest = d.Posts[:3]
	} else {
		d.Latest = d.Posts
	}

	for _, p := range shown {
		d.Page, d.Title = p.File, p.Title
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, p.Template, d); err != nil {
			return nil, fmt.Errorf("render %s: %w", p.File, err)
		}
		out[p.File] = buf.Bytes()
	}
	for _, p := range d.Posts {
		d.Page, d.Title, d.Post = "blog.html", p.Title, p
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "post", d); err != nil {
			return nil, fmt.Errorf("render %s: %w", p.File, err)
		}
		out[p.File] = buf.Bytes()
	}
	return out, nil
}
