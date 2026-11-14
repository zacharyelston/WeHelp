package server

import "testing"

func TestValidTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"scheduled", "cancelled", true},
		{"scheduled", "completed", true},
		{"scheduled", "no_show", true},
		{"scheduled", "scheduled", false},
		{"scheduled", "bogus", false},
		{"cancelled", "scheduled", false},
		{"completed", "scheduled", false},
		{"completed", "cancelled", false},
		{"no_show", "completed", false},
	}
	for _, c := range cases {
		if got := validTransition(c.from, c.to); got != c.want {
			t.Errorf("validTransition(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
