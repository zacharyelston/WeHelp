// Package site renders a practice's public website (#97, #5): about us,
// provider bios, services, how to get the app and join, a library of
// podcast links and downloads, and the supply catalogue. The Back Office
// publishes the result to its Relay as a tar bundle.
//
// Only practice content goes in. Render takes plain values built from the
// site, file and product tables; it never sees a patient table, and the
// pages have no scripts, forms or cookies. Ordering happens only in the app.
package site

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zacharyelston/wehelp/internal/practice"
)

// Limits the Relay also enforces (internal/relay/site.go keeps its own copy:
// the Relay imports no Back Office package).
const (
	MaxBundleBytes = 25 << 20
	MaxFiles       = 200
	MaxFileBytes   = 10 << 20
)

// AllowedTypes maps the file extensions a site may contain to their
// content types. Anything else is refused at upload and by the Relay.
var AllowedTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".ico":  "image/x-icon",
}

// Theme is a colour scheme for the website (#104). The CSS for each is in
// templates/style.css, keyed by data-theme on <html>.
type Theme struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Swatch string `json:"swatch"` // the accent, for the picker
}

// Themes are the choices, default first.
var Themes = []Theme{
	{"sage", "Sage green", "#2f6f4f"},
	{"ocean", "Ocean blue", "#1c6ea4"},
	{"sunset", "Sunset coral", "#c4552d"},
	{"plum", "Plum", "#7b3f8c"},
	{"clay", "Warm clay", "#8a5a3c"},
	{"midnight", "Midnight (dark)", "#2d4a7a"},
}

// ThemeID returns the theme to render: c.Theme if known, else the default.
func (c Content) ThemeID() string {
	for _, t := range Themes {
		if t.ID == c.Theme {
			return t.ID
		}
	}
	return Themes[0].ID
}

// Content is what staff edit on the Website page.
type Content struct {
	PracticeName string     `json:"practice_name"`
	Tagline      string     `json:"tagline"`
	About        string     `json:"about"`
	Hours        string     `json:"hours"`
	Address      string     `json:"address"`
	Phone        string     `json:"phone"`
	Email        string     `json:"email"`
	AppURL       string     `json:"app_url"` // App Store link
	Join         string     `json:"join"`    // extra words for "Get the app and join"
	LogoFile     string     `json:"logo_file"`
	Theme        string     `json:"theme"` // one of Themes; empty = the first
	// Words come from the practice type (#108), set by the Back Office when it
	// renders; never saved with the content.
	Words practice.Words `json:"-"`
	// Off lists pages the practice has switched off (#109): "store",
	// "library". Set by the Back Office when it renders; never saved.
	Off map[string]bool `json:"-"`
	Providers    []Provider `json:"providers"`
	Services     []Service  `json:"services"`
	Podcasts     []Podcast  `json:"podcasts"`
}

type Provider struct {
	Name        string `json:"name"`
	Credentials string `json:"credentials"`
	Bio         string `json:"bio"`
	PhotoFile   string `json:"photo_file"` // an uploaded image's filename
}

type Service struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Podcast struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// File is an uploaded library download or image.
type File struct {
	Kind        string // library or image
	Title       string
	Filename    string
	ContentType string
	Data        []byte
}

// Product is one catalogue item.
type Product struct {
	SKU, Name, Description, Category, Unit string
	PriceCents                             int
}

var filenameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// CleanFilename turns an uploaded name into a safe lower-case one with an
// allowed extension, or returns an error.
func CleanFilename(name string) (string, error) {
	name = strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	name = strings.TrimLeft(b.String(), ".-_")
	if len(name) > 100 {
		ext := path.Ext(name)
		name = name[:100-len(ext)] + ext
	}
	if !filenameRE.MatchString(name) {
		return "", errors.New("file name must have letters or digits")
	}
	if _, ok := AllowedTypes[path.Ext(name)]; !ok || path.Ext(name) == ".html" || path.Ext(name) == ".css" {
		return "", errors.New("upload a PDF, PNG, JPEG, WebP, ICO or text file")
	}
	return name, nil
}

// Validate checks lengths and links. Podcast and app links must be https.
func (c *Content) Validate() error {
	field := func(name, v string, max int) error {
		if !utf8.ValidString(v) || len(v) > max {
			return fmt.Errorf("%s is limited to %d characters", name, max)
		}
		return nil
	}
	link := func(name, v string) error {
		if v == "" {
			return nil
		}
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.Host == "" || len(v) > 500 {
			return fmt.Errorf("%s must be an https:// link", name)
		}
		return nil
	}
	for _, f := range []struct {
		name, v string
		max     int
	}{
		{"practice name", c.PracticeName, 120}, {"tagline", c.Tagline, 200}, {"about", c.About, 5000},
		{"hours", c.Hours, 500}, {"address", c.Address, 300}, {"phone", c.Phone, 40}, {"email", c.Email, 120},
		{"join", c.Join, 2000}, {"logo", c.LogoFile, 100},
	} {
		if err := field(f.name, f.v, f.max); err != nil {
			return err
		}
	}
	if c.Theme != "" && c.ThemeID() != c.Theme {
		return fmt.Errorf("unknown theme %q", c.Theme)
	}
	if err := link("app link", c.AppURL); err != nil {
		return err
	}
	if len(c.Providers) > 30 || len(c.Services) > 50 || len(c.Podcasts) > 50 {
		return errors.New("too many providers, services or podcasts")
	}
	for _, p := range c.Providers {
		if p.Name == "" {
			return errors.New("every provider needs a name")
		}
		for _, err := range []error{field("provider name", p.Name, 120), field("credentials", p.Credentials, 120),
			field("bio", p.Bio, 3000), field("photo", p.PhotoFile, 100)} {
			if err != nil {
				return err
			}
		}
	}
	for _, s := range c.Services {
		if s.Name == "" {
			return errors.New("every service needs a name")
		}
		if err := errors.Join(field("service", s.Name, 120), field("service description", s.Description, 2000)); err != nil {
			return err
		}
	}
	for _, p := range c.Podcasts {
		if p.Title == "" {
			return errors.New("every podcast needs a title")
		}
		if err := errors.Join(field("podcast title", p.Title, 200), field("podcast description", p.Description, 1000),
			link("podcast link", p.URL)); err != nil {
			return err
		}
		if p.URL == "" {
			return errors.New("every podcast needs a link")
		}
	}
	return nil
}

// Bundle renders the site and packs it as a tar. now stamps the footer.
func Bundle(c Content, products []Product, files []File, now time.Time) ([]byte, int, error) {
	pages, err := Render(c, products, files, now)
	if err != nil {
		return nil, 0, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(pages))
	for n := range pages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		data := pages[n]
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(data)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return nil, 0, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, 0, err
	}
	if buf.Len() > MaxBundleBytes || len(pages) > MaxFiles {
		return nil, 0, fmt.Errorf("the site is too large (%d files, %d MB); remove some downloads", len(pages), buf.Len()>>20)
	}
	return buf.Bytes(), len(pages), nil
}
