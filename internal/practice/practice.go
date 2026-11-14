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
	// Off are the features this type starts with turned off (#109).
	Off []string `json:"off"`
}

// Types are the presets, default first. Fixed words, not free text, keep
// translations and support simple.
var Types = []Type{
	{"naturopath", "Naturopathic practice", true, Words{"patient", "patients", "provider", "providers"}, nil},
	{"therapy", "Therapy and psychology", true, Words{"client", "clients", "therapist", "therapists"}, []string{"store", "checkins", "careteam"}},
	{"allied_health", "Allied health (physio, chiro, dietitian)", true, Words{"patient", "patients", "practitioner", "practitioners"}, nil},
	{"salon", "Salon and personal services", false, Words{"client", "clients", "stylist", "stylists"}, []string{"checkins", "video", "library", "careteam"}},
}

// Feature is a practice switch (#109).
type Feature struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Features are the switches an admin can turn off, in Settings order.
var Features = []Feature{
	{"messages", "Messages"},
	{"requests", "Appointment requests"},
	{"reminders", "Appointment reminders"},
	{"checkins", "Check-in reminders"},
	{"video", "Video visits"},
	{"store", "Supply store"},
	{"library", "Library"},
	{"website", "Website"},
	{"careteam", "Care teams"},
}

// IsFeature reports whether id names a switch.
func IsFeature(id string) bool {
	for _, f := range Features {
		if f.ID == id {
			return true
		}
	}
	return false
}

// FeatureLabel returns a switch's label.
func FeatureLabel(id string) string {
	for _, f := range Features {
		if f.ID == id {
			return f.Label
		}
	}
	return id
}

// Effective returns every switch for a practice of type t with the admin's
// overrides applied. A practice that isn't clinical never has check-ins.
func Effective(t Type, overrides map[string]bool) map[string]bool {
	on := map[string]bool{}
	for _, f := range Features {
		on[f.ID] = true
	}
	for _, f := range t.Off {
		on[f] = false
	}
	for k, v := range overrides {
		if IsFeature(k) {
			on[k] = v
		}
	}
	if !t.Clinical {
		on["checkins"] = false
		on["careteam"] = false // care teams are clinical (#111)
	}
	return on
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
