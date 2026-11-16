//go:build demo

package server

import (
	"encoding/json"

	"github.com/zacharyelston/wehelp/internal/seed"
)

// Demo builds only (#161): the fixed demo logins for the sign-in page.
func init() {
	demoBuild = true
	logins, _ := json.Marshal([]map[string]string{
		{"label": "provider", "email": seed.ProviderEmail, "password": seed.ProviderPassword},
		{"label": "admin", "email": seed.AdminEmail, "password": seed.AdminPassword},
		{"label": "front desk", "email": seed.OfficeEmail, "password": seed.OfficePassword},
	})
	demoLoginsJS = "window.WEHELP_DEMO_LOGINS = " + string(logins) + ";\n"
}
