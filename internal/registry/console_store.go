package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/ssh"
)

// Console data (#61): practice details, providers, Relay capacity and
// health. Every change is audited in the Registry's chain like the CLI's.

var (
	ErrInstalled = errors.New("registry: the practice's office is installed")
	ErrDuplicate = errors.New("registry: already exists")
)

var subscriptions = []string{"trial", "active", "past_due", "cancelled"}

// PracticeDetail is a practice as the console shows it.
type PracticeDetail struct {
	Practice
	RelayID      uuid.UUID  `json:"relay_id"`
	ContactName  string     `json:"contact_name"`
	ContactEmail string     `json:"contact_email"`
	ContactPhone string     `json:"contact_phone"`
	Subscription string     `json:"subscription"`
	InstalledAt  *time.Time `json:"installed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Providers    []Provider `json:"providers,omitempty"`
}

type Provider struct {
	ID            uuid.UUID `json:"id"`
	PracticeID    uuid.UUID `json:"practice_id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	Role          string    `json:"role"` // provider or admin
	Credentials   string    `json:"credentials"`
	LicenceNumber string    `json:"licence_number"`
}

const practiceDetailCols = `p.id, p.name, p.status, r.name, o.id, p.relay_id, p.contact_name, p.contact_email,
	p.contact_phone, p.subscription, o.installed_at, p.created_at`

func scanPracticeDetail(row pgx.Row) (PracticeDetail, error) {
	var p PracticeDetail
	err := row.Scan(&p.ID, &p.Name, &p.Status, &p.Relay, &p.OfficeID, &p.RelayID, &p.ContactName, &p.ContactEmail,
		&p.ContactPhone, &p.Subscription, &p.InstalledAt, &p.CreatedAt)
	return p, err
}

func (s *Store) ListPracticeDetails(ctx context.Context) ([]PracticeDetail, error) {
	rows, err := s.pool.Query(ctx, `select `+practiceDetailCols+`
		from practices p join relays r on r.id = p.relay_id left join offices o on o.practice_id = p.id
		order by p.name, p.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PracticeDetail, error) { return scanPracticeDetail(row) })
}

// GetPractice returns one practice with its current providers.
func (s *Store) GetPractice(ctx context.Context, id uuid.UUID) (PracticeDetail, error) {
	p, err := scanPracticeDetail(s.pool.QueryRow(ctx, `select `+practiceDetailCols+`
		from practices p join relays r on r.id = p.relay_id left join offices o on o.practice_id = p.id
		where p.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("%w: practice %s", ErrNotFound, id)
	}
	if err != nil {
		return p, err
	}
	p.Providers, err = s.ListProviders(ctx, id)
	return p, err
}

// PracticeUpdate changes only the fields that are set.
type PracticeUpdate struct {
	Name         *string `json:"name"`
	ContactName  *string `json:"contact_name"`
	ContactEmail *string `json:"contact_email"`
	ContactPhone *string `json:"contact_phone"`
	Subscription *string `json:"subscription"`
}

func checkEmail(field, v string) error {
	if v == "" {
		return nil
	}
	if a, err := mail.ParseAddress(v); err != nil || a.Address != v {
		return fmt.Errorf("%w: %s must be an email address", ErrInvalidInput, field)
	}
	return nil
}

func (s *Store) UpdatePractice(ctx context.Context, actor string, id uuid.UUID, u PracticeUpdate) error {
	trim := func(p *string) {
		if p != nil {
			*p = strings.TrimSpace(*p)
		}
	}
	trim(u.Name)
	trim(u.ContactName)
	trim(u.ContactEmail)
	trim(u.ContactPhone)
	if u.Name != nil && *u.Name == "" {
		return fmt.Errorf("%w: practice name is required", ErrInvalidInput)
	}
	if u.ContactEmail != nil {
		if err := checkEmail("contact email", *u.ContactEmail); err != nil {
			return err
		}
	}
	if u.Subscription != nil && !slices.Contains(subscriptions, *u.Subscription) {
		return fmt.Errorf("%w: subscription must be one of %s", ErrInvalidInput, strings.Join(subscriptions, ", "))
	}
	sets, args, changed := []string{}, []any{id}, []string{}
	add := func(col string, v *string) {
		if v != nil {
			args = append(args, *v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
			changed = append(changed, col)
		}
	}
	add("name", u.Name)
	add("contact_name", u.ContactName)
	add("contact_email", u.ContactEmail)
	add("contact_phone", u.ContactPhone)
	add("subscription", u.Subscription)
	if len(sets) == 0 {
		return fmt.Errorf("%w: nothing to change", ErrInvalidInput)
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update practices set `+strings.Join(sets, ", ")+`, updated_at = now() where id = $1`, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: practice %s", ErrNotFound, id)
		}
		detail := map[string]any{"fields": changed}
		if u.Subscription != nil {
			detail["subscription"] = *u.Subscription
		}
		return record(ctx, tx, Event{Actor: actor, Action: "practice.update", ResourceType: "practice",
			ResourceID: id.String(), Detail: detail})
	})
}

// AssignRelay moves a practice to another Relay. Only before its office is
// installed: an installed office and its paired phones learned the Relay at
// install and pairing, so moving them needs a re-point flow (not in V1).
func (s *Store) AssignRelay(ctx context.Context, actor string, practiceID, relayID uuid.UUID) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var cur uuid.UUID
		var installed bool
		err := tx.QueryRow(ctx, `select p.relay_id, exists(select 1 from offices o where o.practice_id = p.id)
			from practices p where p.id = $1 for update`, practiceID).Scan(&cur, &installed)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: practice %s", ErrNotFound, practiceID)
		}
		if err != nil {
			return err
		}
		if cur == relayID {
			return ErrNoChange
		}
		if installed {
			return ErrInstalled
		}
		var ok bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from relays where id = $1)`, relayID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: relay %s", ErrNotFound, relayID)
		}
		if _, err := tx.Exec(ctx, `update practices set relay_id = $2, updated_at = now() where id = $1`, practiceID, relayID); err != nil {
			return err
		}
		version, err := s.publish(ctx, tx, actor)
		if err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "practice.assign_relay", ResourceType: "practice",
			ResourceID: practiceID.String(), Detail: map[string]any{"from": cur, "to": relayID, "office_list_version": version}})
	})
}

func (s *Store) ListProviders(ctx context.Context, practiceID uuid.UUID) ([]Provider, error) {
	rows, err := s.pool.Query(ctx, `select id, practice_id, name, email::text, role, credentials, licence_number
		from practice_providers where practice_id = $1 and removed_at is null order by role, name`, practiceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Provider, error) {
		var p Provider
		err := row.Scan(&p.ID, &p.PracticeID, &p.Name, &p.Email, &p.Role, &p.Credentials, &p.LicenceNumber)
		return p, err
	})
}

func (p *Provider) check() error {
	p.Name, p.Email = strings.TrimSpace(p.Name), strings.TrimSpace(p.Email)
	p.Credentials, p.LicenceNumber = strings.TrimSpace(p.Credentials), strings.TrimSpace(p.LicenceNumber)
	if p.Name == "" || p.Email == "" {
		return fmt.Errorf("%w: name and email are required", ErrInvalidInput)
	}
	if err := checkEmail("email", p.Email); err != nil {
		return err
	}
	if p.Role != "provider" && p.Role != "admin" {
		return fmt.Errorf("%w: role must be provider or admin", ErrInvalidInput)
	}
	return nil
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func (s *Store) AddProvider(ctx context.Context, actor string, p Provider) (Provider, error) {
	if err := p.check(); err != nil {
		return p, err
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `insert into practice_providers (practice_id, name, email, role, credentials, licence_number)
			values ($1, $2, $3, $4, $5, $6) returning id`,
			p.PracticeID, p.Name, p.Email, p.Role, p.Credentials, p.LicenceNumber).Scan(&p.ID)
		var pg *pgconn.PgError
		switch {
		case isUnique(err):
			return fmt.Errorf("%w: a provider with that email", ErrDuplicate)
		case errors.As(err, &pg) && pg.Code == "23503":
			return fmt.Errorf("%w: practice %s", ErrNotFound, p.PracticeID)
		case err != nil:
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "provider.add", ResourceType: "provider",
			ResourceID: p.ID.String(), Detail: map[string]string{"practice_id": p.PracticeID.String(), "role": p.Role}})
	})
	return p, err
}

func (s *Store) UpdateProvider(ctx context.Context, actor string, p Provider) (Provider, error) {
	if err := p.check(); err != nil {
		return p, err
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update practice_providers set name = $2, email = $3, role = $4, credentials = $5, licence_number = $6
			where id = $1 and removed_at is null returning practice_id`,
			p.ID, p.Name, p.Email, p.Role, p.Credentials, p.LicenceNumber).Scan(&p.PracticeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: provider %s", ErrNotFound, p.ID)
		case isUnique(err):
			return fmt.Errorf("%w: a provider with that email", ErrDuplicate)
		case err != nil:
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "provider.update", ResourceType: "provider",
			ResourceID: p.ID.String(), Detail: map[string]string{"practice_id": p.PracticeID.String(), "role": p.Role}})
	})
	return p, err
}

func (s *Store) RemoveProvider(ctx context.Context, actor string, id uuid.UUID) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var practice uuid.UUID
		err := tx.QueryRow(ctx, `update practice_providers set removed_at = now() where id = $1 and removed_at is null
			returning practice_id`, id).Scan(&practice)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: provider %s", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "provider.remove", ResourceType: "provider",
			ResourceID: id.String(), Detail: map[string]string{"practice_id": practice.String()}})
	})
}

func (s *Store) SetRelayCapacity(ctx context.Context, actor string, id uuid.UUID, capacity int) error {
	if capacity < 1 || capacity > 10000 {
		return fmt.Errorf("%w: capacity must be 1-10000", ErrInvalidInput)
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var from int
		err := tx.QueryRow(ctx, `select capacity from relays where id = $1 for update`, id).Scan(&from)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: relay %s", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update relays set capacity = $2 where id = $1`, id, capacity); err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "relay.set_capacity", ResourceType: "relay",
			ResourceID: id.String(), Detail: map[string]int{"from": from, "to": capacity}})
	})
}

// RelayReport is what a Relay sends every minute (#61): the office list it
// is using and which offices are connected. Office IDs only.
type RelayReport struct {
	RelayID          string   `json:"relay_id"`
	At               int64    `json:"at"` // unix seconds
	ListVersion      int64    `json:"list_version"`
	ConnectedOffices []string `json:"connected_offices"`
}

// RelayReportMaxSkew bounds how old (or early) a report may be, so a
// captured report can't be replayed later.
const RelayReportMaxSkew = 2 * time.Minute

// AcceptRelayReport verifies a report signed with the Relay's pinned SSH
// host key and stores it as the Relay's latest status.
func (s *Store) AcceptRelayReport(ctx context.Context, payload []byte, sig *ssh.Signature) error {
	var rep RelayReport
	if err := json.Unmarshal(payload, &rep); err != nil {
		return fmt.Errorf("%w: report: %v", ErrInvalidInput, err)
	}
	id, err := uuid.Parse(rep.RelayID)
	if err != nil {
		return fmt.Errorf("%w: relay_id", ErrInvalidInput)
	}
	var hostKey string
	err = s.pool.QueryRow(ctx, `select ssh_host_key from relays where id = $1`, id).Scan(&hostKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidToken
	}
	if err != nil {
		return err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil || sig == nil || pub.Verify(payload, sig) != nil {
		return ErrInvalidToken
	}
	at := time.Unix(rep.At, 0)
	if d := s.now().Sub(at); d > RelayReportMaxSkew || d < -RelayReportMaxSkew {
		return fmt.Errorf("%w: report time is off by %s", ErrInvalidInput, d.Round(time.Second))
	}
	for _, o := range rep.ConnectedOffices {
		if _, err := uuid.Parse(o); err != nil {
			return fmt.Errorf("%w: office id", ErrInvalidInput)
		}
	}
	offices, _ := json.Marshal(rep.ConnectedOffices)
	// Only a newer report replaces the stored one.
	_, err = s.pool.Exec(ctx, `
		insert into relay_status (relay_id, reported_at, list_version, connected_offices) values ($1, $2, $3, $4)
		on conflict (relay_id) do update set reported_at = excluded.reported_at, list_version = excluded.list_version,
			connected_offices = excluded.connected_offices
		where relay_status.reported_at < excluded.reported_at`, id, at, rep.ListVersion, offices)
	return err
}

// RelayStaleAfter: a Relay that hasn't reported for this long shows as down.
const RelayStaleAfter = 3 * time.Minute

type RelayHealth struct {
	Relay
	Capacity    int        `json:"capacity"`
	Practices   int        `json:"practices"`
	ReportedAt  *time.Time `json:"reported_at"`
	ListVersion *int64     `json:"list_version"`
	Up          bool       `json:"up"`
	Connected   int        `json:"connected_offices"`
}

type OfficeHealth struct {
	PracticeID   uuid.UUID `json:"practice_id"`
	PracticeName string    `json:"practice_name"`
	OfficeID     uuid.UUID `json:"office_id"`
	Relay        string    `json:"relay"`
	Status       string    `json:"status"` // practice status
	Connected    bool      `json:"connected"`
}

type Health struct {
	LatestList int64          `json:"latest_list_version"`
	Relays     []RelayHealth  `json:"relays"`
	Offices    []OfficeHealth `json:"offices"`
}

// Health reports each Relay's last report and whether each installed office
// is connected to its Relay, from the Relays' own reports.
func (s *Store) Health(ctx context.Context) (Health, error) {
	var h Health
	if err := s.pool.QueryRow(ctx, `select coalesce(max(version), 0) from office_lists`).Scan(&h.LatestList); err != nil {
		return h, err
	}
	rows, err := s.pool.Query(ctx, `
		select r.id, r.name, r.ssh_address, r.connect_address, r.ssh_host_key, r.tls_pin, r.capacity,
		       (select count(*) from practices p where p.relay_id = r.id),
		       st.reported_at, st.list_version, coalesce(st.connected_offices, '[]')
		from relays r left join relay_status st on st.relay_id = r.id
		order by r.name`)
	if err != nil {
		return h, err
	}
	connected := map[string]bool{}
	h.Relays, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RelayHealth, error) {
		var r RelayHealth
		var offices []byte
		err := row.Scan(&r.ID, &r.Name, &r.SSHAddress, &r.ConnectAddress, &r.SSHHostKey, &r.TLSPin, &r.Capacity,
			&r.Practices, &r.ReportedAt, &r.ListVersion, &offices)
		if err != nil {
			return r, err
		}
		r.Up = r.ReportedAt != nil && s.now().Sub(*r.ReportedAt) < RelayStaleAfter
		var ids []string
		_ = json.Unmarshal(offices, &ids)
		r.Connected = len(ids)
		if r.Up {
			for _, id := range ids {
				connected[r.ID.String()+"/"+id] = true
			}
		}
		return r, nil
	})
	if err != nil {
		return h, err
	}
	rows, err = s.pool.Query(ctx, `
		select p.id, p.name, o.id, r.name, p.status, r.id
		from offices o join practices p on p.id = o.practice_id join relays r on r.id = p.relay_id
		order by p.name`)
	if err != nil {
		return h, err
	}
	h.Offices, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OfficeHealth, error) {
		var o OfficeHealth
		var relayID uuid.UUID
		err := row.Scan(&o.PracticeID, &o.PracticeName, &o.OfficeID, &o.Relay, &o.Status, &relayID)
		o.Connected = connected[relayID.String()+"/"+o.OfficeID.String()]
		return o, err
	})
	return h, err
}

// AuditEntry is one event as the console lists it.
type AuditEntry struct {
	Seq          int64           `json:"seq"`
	Actor        string          `json:"actor"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Detail       json.RawMessage `json:"detail"`
	CreatedAt    time.Time       `json:"created_at"`
}

func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.pool.Query(ctx, `select seq, actor, action, resource_type, resource_id, detail, created_at
		from audit_events order by seq desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		err := row.Scan(&e.Seq, &e.Actor, &e.Action, &e.ResourceType, &e.ResourceID, &e.Detail, &e.CreatedAt)
		return e, err
	})
}

// RecordAs writes one audit event in its own transaction (console sign-in).
func (s *Store) RecordAs(ctx context.Context, e Event) error {
	return s.withTx(ctx, func(tx pgx.Tx) error { return record(ctx, tx, e) })
}
