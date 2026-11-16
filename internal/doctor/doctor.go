// Package doctor checks whether a Back Office is ready to hold real patient
// data (#81): the machine-checkable go-live criteria from
// docs/onboarding/RUNBOOK.md. Each check says PASS, FAIL or SKIP with a
// short detail. Details carry counts, IDs and settings, never patient data.
//
// The checks that need the host (FileVault, automatic login, how Postgres is
// published) run in deploy/appliance.sh, outside the container, and are
// passed in with ParseHost. Fixing anything is out of scope: the doctor only
// reports.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Status is a check's outcome.
type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
)

// Check is one line of the report.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Func runs one check.
type Func func(ctx context.Context) Check

// Report is every check, in order.
type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Run runs the checks in order. A check that panics fails rather than
// stopping the report.
func Run(ctx context.Context, checks []Func) Report {
	r := Report{OK: true}
	for _, f := range checks {
		c := safely(ctx, f)
		if c.Status == Fail {
			r.OK = false
		}
		r.Checks = append(r.Checks, c)
	}
	return r
}

func safely(ctx context.Context, f Func) (c Check) {
	defer func() {
		if p := recover(); p != nil {
			c = Check{Name: "check", Status: Fail, Detail: fmt.Sprintf("check crashed: %v", p)}
		}
	}()
	return f(ctx)
}

// Text writes one line per check: "PASS  name  detail".
func (r Report) Text(w io.Writer) {
	width := 0
	for _, c := range r.Checks {
		width = max(width, len(c.Name))
	}
	for _, c := range r.Checks {
		fmt.Fprintf(w, "%-4s  %-*s  %s\n", c.Status, width, c.Name, c.Detail)
	}
	if r.OK {
		fmt.Fprintln(w, "ready: no check failed")
	} else {
		fmt.Fprintln(w, "NOT READY: fix every FAIL before real patient data")
	}
}

// JSON writes the report for the go-live record.
func (r Report) JSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// ParseHost reads host checks from appliance.sh: one per line,
// "name|STATUS|detail". Lines that don't parse become a failed check, so a
// broken script can't pass silently.
func ParseHost(s string) []Func {
	var out []Func
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		c := Check{Name: "host", Status: Fail, Detail: "unreadable host check: " + line}
		if len(parts) >= 2 {
			st := Status(strings.ToUpper(strings.TrimSpace(parts[1])))
			if st == Pass || st == Fail || st == Skip {
				c = Check{Name: strings.TrimSpace(parts[0]), Status: st}
				if len(parts) == 3 {
					c.Detail = strings.TrimSpace(parts[2])
				}
			}
		}
		out = append(out, func(context.Context) Check { return c })
	}
	return out
}
