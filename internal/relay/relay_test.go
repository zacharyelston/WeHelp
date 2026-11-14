package relay

import "testing"

func TestOfficeFromHost(t *testing.T) {
	const id = "5fcd416a-fec0-48ba-a4f2-9b6c7ad43a59"
	ok := []string{id + ".wehelp.invalid:443", "5FCD416A-FEC0-48BA-A4F2-9B6C7AD43A59.wehelp.invalid:443"}
	for _, h := range ok {
		if got, good := officeFromHost(h); !good || got != id {
			t.Errorf("officeFromHost(%q) = %q, %v", h, got, good)
		}
	}
	bad := []string{
		id + ".wehelp.invalid",            // no port
		id + ".wehelp.invalid:22",         // wrong port
		id + ".example.com:443",           // wrong domain
		"x." + id + ".wehelp.invalid:443", // nested
		"../etc.wehelp.invalid:443",
		".wehelp.invalid:443",
		"127.0.0.1:443",
	}
	for _, h := range bad {
		if got, good := officeFromHost(h); good {
			t.Errorf("officeFromHost(%q) accepted as %q", h, got)
		}
	}
}
