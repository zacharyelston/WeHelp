package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Status reports (#61): after each list sync the Relay tells the Registry
// which list it is using and which offices are connected, signed with its
// SSH host key (the key the Registry already pins for it). Office IDs only:
// no addresses, no phones, no traffic.

// SignatureHeader matches registry.RelaySignatureHeader.
const SignatureHeader = "X-WeHelp-Relay-Signature"

type statusReport struct {
	RelayID          string   `json:"relay_id"`
	At               int64    `json:"at"`
	ListVersion      int64    `json:"list_version"`
	ConnectedOffices []string `json:"connected_offices"`
}

// ConnectedOffices lists the offices with a live connection, sorted.
func (r *Relay) ConnectedOffices() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.offices))
	for id := range r.offices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Report sends one signed status report to the Registry.
func (r *Relay) Report(ctx context.Context) error {
	payload, err := json.Marshal(statusReport{RelayID: r.cfg.RelayID, At: time.Now().Unix(),
		ListVersion: r.Version(), ConnectedOffices: r.ConnectedOffices()})
	if err != nil {
		return err
	}
	sig, err := r.cfg.HostKey.Sign(rand.Reader, payload)
	if err != nil {
		return err
	}
	url := strings.TrimRight(r.cfg.RegistryURL, "/") + "/v1/relay-status"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, base64.StdEncoding.EncodeToString(ssh.Marshal(sig)))
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("relay: status report: HTTP %d", resp.StatusCode)
	}
	return nil
}
