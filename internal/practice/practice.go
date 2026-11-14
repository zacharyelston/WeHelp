// Package practice holds the practice-type presets (#108): the words each
// kind of practice uses for the people it serves and its staff, and
// whether it is clinical. The API and the database always say patient and
// provider; the staff web, the website and the app show these words.
// See docs/PRACTICE_TYPES.md.
package practice

// Words are what a practice calls the people it serves and the staff who
// see them. Each has a singular and plural, lower case; callers capitalise.
type Words struct {
	Patient   string `json:"patient"`
	Patients  string `json:"patients"`
	Provider  string `json:"provider"`
	Providers string `json:"providers"`
}

// Type is a preset.
type Type struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Clinical bool   `json:"clinical"` // false hides check-ins and health monitors
	Words    Words  `json:"words"`
}

// Types are the presets, default first. Fixed words, not free text, keep
// translations and support simple.
var Types = []Type{
	{"naturopath", "Naturopathic practice", true, Words{"patient", "patients", "provider", "providers"}},
	{"therapy", "Therapy and psychology", true, Words{"client", "clients", "therapist", "therapists"}},
	{"allied_health", "Allied health (physio, chiro, dietitian)", true, Words{"patient", "patients", "practitioner", "practitioners"}},
	{"salon", "Salon and personal services", false, Words{"client", "clients", "stylist", "stylists"}},
}

// Default is the preset for a practice that hasn't chosen one.
var Default = Types[0]

// Lookup returns the preset with this id.
func Lookup(id string) (Type, bool) {
	for _, t := range Types {
		if t.ID == id {
			return t, true
		}
	}
	return Type{}, false
}
