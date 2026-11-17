// Package templates holds the starter forms the builder offers under "New
// form" (#258): a new patient intake, a follow-up visit and a symptom
// checklist. They are synthetic and practice-neutral, not validated
// instruments, and every one is a valid wehelp.form.v1 definition.
package templates

import (
	"embed"
	"fmt"

	"github.com/zacharyelston/wehelp/internal/forms"
)

//go:embed *.json
var files embed.FS

// Template is one starter form.
type Template struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Canonical   []byte `json:"-"`
}

// keys is the order "New form" lists them in.
var keys = []string{"intake", "followup", "symptoms"}

var all []Template

func init() {
	for _, k := range keys {
		raw, err := files.ReadFile(k + ".json")
		if err != nil {
			panic(err)
		}
		def, err := forms.Parse(raw)
		if err != nil {
			panic(fmt.Sprintf("templates: %s: %v", k, err))
		}
		canon, err := forms.Canonical(raw)
		if err != nil {
			panic(err)
		}
		all = append(all, Template{Key: k, Name: def.Title, Description: def.Description, Canonical: canon})
	}
}

// All returns the templates in menu order.
func All() []Template { return append([]Template(nil), all...) }

// Get returns the template with this key.
func Get(key string) (Template, bool) {
	for _, t := range all {
		if t.Key == key {
			return t, true
		}
	}
	return Template{}, false
}
