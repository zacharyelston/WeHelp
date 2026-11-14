package office

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zacharyelston/wehelp/internal/officelist"
	"golang.org/x/crypto/ssh"
)

// Install redeems an install token with the Registry (#60): it sends the
// office SSH public key and stores the office ID, Relay address and host key,
// and the Registry signing key it gets back.
func (id *Identity) Install(ctx context.Context, client *http.Client, registryURL, token string) (Registration, error) {
	u, err := url.Parse(strings.TrimRight(registryURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Registration{}, fmt.Errorf("office: registry URL must be http(s)://host[:port], got %q", registryURL)
	}
	body, _ := json.Marshal(map[string]string{
		"install_token":  strings.TrimSpace(token),
		"ssh_public_key": id.SSHAuthorizedKey(),
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String()+"/v1/install", bytes.NewReader(body))
	if err != nil {
		return Registration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Registration{}, fmt.Errorf("office: contact Registry: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Registration{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return Registration{}, fmt.Errorf("office: Registry refused install (%d): %s", resp.StatusCode, e.Error)
	}
	var r Registration
	if err := json.Unmarshal(raw, &r); err != nil {
		return Registration{}, fmt.Errorf("office: Registry response: %w", err)
	}
	if err := r.validate(); err != nil {
		return Registration{}, err
	}
	r.RegistryURL = u.String()
	r.InstalledAt = time.Now().UTC().Truncate(time.Second)
	if err := id.SaveRegistration(r); err != nil {
		return Registration{}, err
	}
	return r, nil
}

// validate rejects a response the office could not safely pin.
func (r Registration) validate() error {
	if r.OfficeID == "" || strings.ContainsAny(r.OfficeID, "./: ") {
		return fmt.Errorf("office: Registry returned a bad office ID %q", r.OfficeID)
	}
	if _, err := officelist.ParsePublicKey(r.RegistrySigningKey); err != nil {
		return fmt.Errorf("office: Registry signing key: %w", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(r.Relay.SSHHostKey)); err != nil {
		return fmt.Errorf("office: Relay host key: %w", err)
	}
	if pin, err := base64.StdEncoding.DecodeString(r.Relay.TLSPin); err != nil || len(pin) != 32 {
		return fmt.Errorf("office: Registry returned no valid Relay TLS pin")
	}
	if r.Relay.SSHAddress == "" || r.Relay.ConnectAddress == "" {
		return fmt.Errorf("office: Registry returned no Relay address")
	}
	return nil
}
