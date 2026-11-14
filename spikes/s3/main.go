// Command s3 is Spike S3 (#34): send a push from the Relay with the team's
// APNs auth key, using only fixed, PHI-free categories. Throwaway code; see
// docs/spikes/S3.md.
//
//	s3 -key AuthKey_XXXX.p8 -key-id XXXX -team WZYFJ8TSWD \
//	   -topic com.zacelston.wehelp.spike3 -token <device token> -category new_message
//
// The key file is read from disk and never printed.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

// categories is the Relay's whole vocabulary. The Back Office can name a
// category and a device token, nothing else, so no patient data can reach
// Apple or the Relay through a push.
var categories = map[string]string{
	"new_message":          "You have a new message",
	"appointment_reminder": "You have an upcoming appointment",
	"visit_starting":       "Your visit is starting",
}

func main() {
	keyPath := flag.String("key", "", "APNs auth key (.p8)")
	keyID := flag.String("key-id", "", "APNs key ID")
	team := flag.String("team", "WZYFJ8TSWD", "Apple team ID")
	topic := flag.String("topic", "com.zacelston.wehelp.spike3", "app bundle ID")
	deviceToken := flag.String("token", "", "device token (hex)")
	category := flag.String("category", "new_message", "one of: new_message, appointment_reminder, visit_starting")
	production := flag.Bool("production", false, "use the production APNs host (default: sandbox)")
	flag.Parse()

	body, ok := categories[*category]
	if !ok {
		fail("refused: %q is not an allowed category", *category)
	}
	if *keyPath == "" || *keyID == "" || *deviceToken == "" {
		fail("need -key, -key-id and -token")
	}
	authKey, err := token.AuthKeyFromFile(*keyPath)
	if err != nil {
		fail("reading key: %v", err)
	}
	tok := &token.Token{AuthKey: authKey, KeyID: *keyID, TeamID: *team}
	client := apns2.NewTokenClient(tok).Development()
	if *production {
		client = client.Production()
	}

	n := &apns2.Notification{
		DeviceToken: *deviceToken,
		Topic:       *topic,
		Expiration:  time.Now().Add(time.Hour),
		Priority:    apns2.PriorityHigh,
		// Generic text and the category only. The app fetches details over
		// its pinned connection to the Back Office.
		Payload: payload.NewPayload().AlertTitle("WeHelp").AlertBody(body).Category(*category).Sound("default").Custom("cat", *category),
	}
	start := time.Now()
	res, err := client.Push(n)
	if err != nil {
		fail("push failed: %v", err)
	}
	fmt.Printf("status=%d apns-id=%s reason=%q sent=%v latency=%s\n", res.StatusCode, res.ApnsID, res.Reason, res.Sent(), time.Since(start).Round(time.Millisecond))
	if !res.Sent() {
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
