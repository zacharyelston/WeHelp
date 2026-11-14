// Package registry is the WeHelp Registry (#60): practices, Relays, offices,
// install tokens and the signed office list. It holds business data only and
// must never import a Back Office data package (see boundary_test.go).
package registry

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"golang.org/x/crypto/ssh"
)

// InstallTokenTTL is how long an install token stays valid.
const InstallTokenTTL = 24 * time.Hour

const tokenPrefix = "wht_"

var (
	ErrNotFound      = errors.New("registry: not found")
	ErrNoChange      = errors.New("registry: already in that state")
	ErrInvalidToken  = errors.New("registry: invalid install token")
	ErrKeyInUse      = errors.New("registry: SSH key already belongs to another office")
	ErrInvalidInput  = errors.New("registry: invalid input")
	ErrNotActive     = errors.New("registry: practice is suspended")
	errNoSigningKeys = errors.New("registry: no signing key configured")
)

// Store is the Registry's data layer. Every change runs in one transaction
// with its audit event, and changes that affect which offices may connect
// also publish a new signed office list in that transaction.
type Store struct {
	pool *pgxpool.Pool
	key  ed25519.PrivateKey // nil for read-only use
	now  func() time.Time
}

// New returns a Store. signingKey may be nil for commands that never
// publish a list.
func New(pool *pgxpool.Pool, signingKey ed25519.PrivateKey) *Store {
	return &Store{pool: pool, key: signingKey, now: time.Now}
}

type Relay struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	SSHAddress     string    `json:"ssh_address"`
	ConnectAddress string    `json:"connect_address"`
	SSHHostKey     string    `json:"ssh_host_key"`
	TLSPin         string    `json:"tls_pin"` // phones pin the Relay's TLS key (keyfile.P256Pin)
}

type Practice struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	Status   string     `json:"status"`
	Relay    string     `json:"relay"`
	OfficeID *uuid.UUID `json:"office_id,omitempty"`
}

// InstallResult is what an office learns when it installs.
type InstallResult struct {
	OfficeID           uuid.UUID `json:"office_id"`
	Relay              Relay     `json:"relay"`
	RegistrySigningKey string    `json:"registry_signing_key"`
}

func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// normalizeEd25519Key accepts an authorized_keys line and returns it in
// canonical form without a comment. Only ssh-ed25519 keys are accepted.
func normalizeEd25519Key(line string) (string, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return "", fmt.Errorf("%w: SSH public key: %v", ErrInvalidInput, err)
	}
	if pk.Type() != ssh.KeyAlgoED25519 {
		return "", fmt.Errorf("%w: SSH key must be ssh-ed25519, got %s", ErrInvalidInput, pk.Type())
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), nil
}

func checkHostPort(field, v string) error {
	if _, _, err := net.SplitHostPort(v); err != nil {
		return fmt.Errorf("%w: %s must be host:port: %v", ErrInvalidInput, field, err)
	}
	return nil
}

// AddRelay registers a Relay. Host keys may be any SSH key type the Relay
// uses; offices pin exactly this key.
func (s *Store) AddRelay(ctx context.Context, actor string, r Relay) (Relay, error) {
	if strings.TrimSpace(r.Name) == "" {
		return Relay{}, fmt.Errorf("%w: relay name is required", ErrInvalidInput)
	}
	if err := checkHostPort("ssh address", r.SSHAddress); err != nil {
		return Relay{}, err
	}
	if err := checkHostPort("connect address", r.ConnectAddress); err != nil {
		return Relay{}, err
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(r.SSHHostKey)))
	if err != nil {
		return Relay{}, fmt.Errorf("%w: relay host key: %v", ErrInvalidInput, err)
	}
	r.SSHHostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	if err := checkPin(r.TLSPin); err != nil {
		return Relay{}, err
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			insert into relays (name, ssh_address, connect_address, ssh_host_key, tls_pin)
			values ($1, $2, $3, $4, $5) returning id`,
			r.Name, r.SSHAddress, r.ConnectAddress, r.SSHHostKey, r.TLSPin).Scan(&r.ID); err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "relay.create", ResourceType: "relay",
			ResourceID: r.ID.String(), Detail: map[string]string{
				"name": r.Name, "ssh_address": r.SSHAddress, "connect_address": r.ConnectAddress,
				"host_key_fingerprint": ssh.FingerprintSHA256(pk), "tls_pin": r.TLSPin}})
	})
	return r, err
}

// checkPin accepts a base64 SHA-256 pin.
func checkPin(pin string) error {
	raw, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(raw) != sha256.Size {
		return fmt.Errorf("%w: relay TLS pin must be base64 SHA-256 (wehelp-relay init prints it)", ErrInvalidInput)
	}
	return nil
}

// SetRelayTLSPin records a Relay's TLS pin (after the Relay's TLS key is
// created or rotated). Offices learn it at their next install.
func (s *Store) SetRelayTLSPin(ctx context.Context, actor, name, pin string) error {
	if err := checkPin(pin); err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var id uuid.UUID
		var old string
		err := tx.QueryRow(ctx, `select id, tls_pin from relays where name = $1 for update`, name).Scan(&id, &old)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: relay %q", ErrNotFound, name)
		}
		if err != nil {
			return err
		}
		if old == pin {
			return ErrNoChange
		}
		if _, err := tx.Exec(ctx, `update relays set tls_pin = $2 where id = $1`, id, pin); err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "relay.set_tls_pin", ResourceType: "relay",
			ResourceID: id.String(), Detail: map[string]string{"from": old, "to": pin}})
	})
}

func (s *Store) ListRelays(ctx context.Context) ([]Relay, error) {
	rows, err := s.pool.Query(ctx, `select id, name, ssh_address, connect_address, ssh_host_key, tls_pin from relays order by name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Relay, error) {
		var r Relay
		err := row.Scan(&r.ID, &r.Name, &r.SSHAddress, &r.ConnectAddress, &r.SSHHostKey, &r.TLSPin)
		return r, err
	})
}

// AddPractice registers a practice assigned to the named Relay. It has no
// office until it installs with a token, so no list is published.
func (s *Store) AddPractice(ctx context.Context, actor, name, relayName string) (Practice, error) {
	p := Practice{Name: strings.TrimSpace(name), Status: "active", Relay: relayName}
	if p.Name == "" {
		return Practice{}, fmt.Errorf("%w: practice name is required", ErrInvalidInput)
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var relayID uuid.UUID
		err := tx.QueryRow(ctx, `select id from relays where name = $1`, relayName).Scan(&relayID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: relay %q", ErrNotFound, relayName)
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into practices (name, relay_id) values ($1, $2) returning id`,
			p.Name, relayID).Scan(&p.ID); err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "practice.create", ResourceType: "practice",
			ResourceID: p.ID.String(), Detail: map[string]string{"name": p.Name, "relay_id": relayID.String()}})
	})
	return p, err
}

func (s *Store) ListPractices(ctx context.Context) ([]Practice, error) {
	rows, err := s.pool.Query(ctx, `
		select p.id, p.name, p.status, r.name, o.id
		from practices p join relays r on r.id = p.relay_id
		left join offices o on o.practice_id = p.id
		order by p.name, p.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Practice, error) {
		var p Practice
		err := row.Scan(&p.ID, &p.Name, &p.Status, &p.Relay, &p.OfficeID)
		return p, err
	})
}

// SetPracticeStatus suspends ("suspended") or reactivates ("active") a
// practice and publishes a new office list without (or with) its office.
func (s *Store) SetPracticeStatus(ctx context.Context, actor string, id uuid.UUID, status string) error {
	if status != "active" && status != "suspended" {
		return fmt.Errorf("%w: status must be active or suspended", ErrInvalidInput)
	}
	action := map[string]string{"active": "practice.reactivate", "suspended": "practice.suspend"}[status]
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `select status from practices where id = $1 for update`, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: practice %s", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		if cur == status {
			return ErrNoChange
		}
		if _, err := tx.Exec(ctx, `update practices set status = $2, updated_at = now() where id = $1`, id, status); err != nil {
			return err
		}
		version, err := s.publish(ctx, tx, actor)
		if err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: action, ResourceType: "practice",
			ResourceID: id.String(), Detail: map[string]any{"from": cur, "to": status, "office_list_version": version}})
	})
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// IssueInstallToken creates a single-use install token for an active
// practice. The token is returned once and only its hash is stored.
func (s *Store) IssueInstallToken(ctx context.Context, actor string, practiceID uuid.UUID) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	expires = s.now().Add(InstallTokenTTL).UTC().Truncate(time.Second)
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `select status from practices where id = $1`, practiceID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: practice %s", ErrNotFound, practiceID)
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return ErrNotActive
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
			insert into install_tokens (practice_id, token_hash, expires_at, created_by)
			values ($1, $2, $3, $4) returning id`,
			practiceID, hashToken(token), expires, actor).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "install_token.issue", ResourceType: "install_token",
			ResourceID: id.String(), Detail: map[string]string{
				"practice_id": practiceID.String(), "expires_at": expires.Format(time.RFC3339)}})
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Install redeems an install token with the office's SSH public key. It
// creates the practice's office (or, on reinstall, replaces its key and
// keeps the office ID), publishes a new office list, and returns what the
// office needs to reach its Relay. Every token failure is ErrInvalidToken;
// the reason is returned separately for server-side logs only.
func (s *Store) Install(ctx context.Context, token, sshPublicKey string) (res InstallResult, reason string, err error) {
	if s.key == nil {
		return res, "", errNoSigningKeys
	}
	key, err := normalizeEd25519Key(sshPublicKey)
	if err != nil {
		return res, "", err
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return res, "malformed", ErrInvalidToken
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var (
			tokenID, practiceID uuid.UUID
			expires             time.Time
			used                *time.Time
			status              string
		)
		err := tx.QueryRow(ctx, `
			select t.id, t.practice_id, t.expires_at, t.used_at, p.status
			from install_tokens t join practices p on p.id = t.practice_id
			where t.token_hash = $1 for update of t`, hashToken(token)).
			Scan(&tokenID, &practiceID, &expires, &used, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			reason = "unknown"
			return ErrInvalidToken
		case err != nil:
			return err
		case used != nil:
			reason = "already used: " + tokenID.String()
			return ErrInvalidToken
		case !s.now().Before(expires):
			reason = "expired: " + tokenID.String()
			return ErrInvalidToken
		case status != "active":
			reason = "practice suspended: " + tokenID.String()
			return ErrInvalidToken
		}
		if _, err := tx.Exec(ctx, `update install_tokens set used_at = now() where id = $1`, tokenID); err != nil {
			return err
		}

		var reinstall bool
		err = tx.QueryRow(ctx, `select true from offices where practice_id = $1`, practiceID).Scan(&reinstall)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `
			insert into offices (practice_id, ssh_public_key) values ($1, $2)
			on conflict (practice_id) do update
				set ssh_public_key = excluded.ssh_public_key, installed_at = now()
			returning id`, practiceID, key).Scan(&res.OfficeID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrKeyInUse
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			select r.id, r.name, r.ssh_address, r.connect_address, r.ssh_host_key, r.tls_pin
			from relays r join practices p on p.relay_id = r.id where p.id = $1`, practiceID).
			Scan(&res.Relay.ID, &res.Relay.Name, &res.Relay.SSHAddress, &res.Relay.ConnectAddress, &res.Relay.SSHHostKey, &res.Relay.TLSPin); err != nil {
			return err
		}
		actor := "install-token:" + tokenID.String()
		version, err := s.publish(ctx, tx, actor)
		if err != nil {
			return err
		}
		pk, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key))
		return record(ctx, tx, Event{Actor: actor, Action: "office.install", ResourceType: "office",
			ResourceID: res.OfficeID.String(), Detail: map[string]any{
				"practice_id": practiceID.String(), "key_fingerprint": ssh.FingerprintSHA256(pk),
				"reinstall": reinstall, "office_list_version": version}})
	})
	if err != nil {
		return InstallResult{}, reason, err
	}
	res.RegistrySigningKey = officelist.PublicKeyString(s.key.Public().(ed25519.PublicKey))
	return res, "", nil
}

// publish signs and stores a new office list from the current state, inside
// the caller's transaction. Versions are serialized by an advisory lock.
func (s *Store) publish(ctx context.Context, tx pgx.Tx, actor string) (int64, error) {
	if s.key == nil {
		return 0, errNoSigningKeys
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, lockPublish); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
		select o.id::text, o.ssh_public_key, p.relay_id::text
		from offices o join practices p on p.id = o.practice_id
		where p.status = 'active'
		order by o.id`)
	if err != nil {
		return 0, err
	}
	offices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (officelist.Office, error) {
		var o officelist.Office
		err := row.Scan(&o.OfficeID, &o.SSHPublicKey, &o.RelayID)
		return o, err
	})
	if err != nil {
		return 0, err
	}
	var version int64
	if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) + 1 from office_lists`).Scan(&version); err != nil {
		return 0, err
	}
	payload, sig, err := officelist.Sign(s.key, officelist.List{
		Version: version, IssuedAt: s.now().UTC().Truncate(time.Second), Offices: offices})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `insert into office_lists (version, payload, signature) values ($1, $2, $3)`,
		version, payload, sig); err != nil {
		return 0, err
	}
	return version, record(ctx, tx, Event{Actor: actor, Action: "office_list.publish", ResourceType: "office_list",
		ResourceID: fmt.Sprint(version), Detail: map[string]int{"offices": len(offices)}})
}

// EnsureList publishes version 1 (empty or not) if no list exists yet, so
// Relays always have something signed to fetch.
func (s *Store) EnsureList(ctx context.Context) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, lockPublish); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from office_lists)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		_, err := s.publish(ctx, tx, "system")
		return err
	})
}

// LatestList returns the newest signed list.
func (s *Store) LatestList(ctx context.Context) (officelist.Envelope, error) {
	var payload, sig []byte
	err := s.pool.QueryRow(ctx, `select payload, signature from office_lists order by version desc limit 1`).
		Scan(&payload, &sig)
	if errors.Is(err, pgx.ErrNoRows) {
		return officelist.Envelope{}, ErrNotFound
	}
	if err != nil {
		return officelist.Envelope{}, err
	}
	return officelist.Seal(payload, sig), nil
}
