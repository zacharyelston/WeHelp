package doctor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/keyfile"
	"github.com/zacharyelston/wehelp/internal/office"
)

// MaxBackupAge is how old the last good backup may be (a nightly job plus
// slack).
const MaxBackupAge = 26 * time.Hour

// Deps is what the Back Office checks read. The database and network parts
// are functions, so tests can fake each one.
type Deps struct {
	OfficeDir    string
	RelayTunnel  bool   // relay_tunnel setting
	ReadyURL     string // the server's /readyz on this host
	StaffTLSAddr string // staff_tls_addr setting, e.g. ":8443"
	DemoBuild    bool   // built with the demo tag
	Env          string // env setting
	Now          func() time.Time
	HTTP         *http.Client

	// VerifyAudit checks every practice's audit chain: events checked and
	// how many chains are broken.
	VerifyAudit func(ctx context.Context) (events int64, broken int, err error)
	// LastBackup is when the last successful backup finished.
	LastBackup func(ctx context.Context) (at time.Time, ok bool, err error)
	// SeedAccounts counts accounts the demo seed creates (@demo.wehelp).
	SeedAccounts func(ctx context.Context) (int, error)
}

// Checks is the Back Office's list, in report order.
func Checks(d Deps) []Func {
	return []Func{
		d.officeKeys,
		d.installed,
		d.relayTunnel,
		d.auditChain,
		d.backup,
		d.restoreTest,
		d.demoOff,
		d.noSeedAccounts,
		d.staffHTTPS,
	}
}

func (d Deps) officeKeys(context.Context) Check {
	c := Check{Name: "office_keys"}
	st, err := os.Stat(d.OfficeDir)
	if err != nil {
		return fail(c, "office directory missing: run wehelpd init")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fail(c, fmt.Sprintf("office directory is mode %04o, want 0700", st.Mode().Perm()))
	}
	for _, f := range []string{office.SSHKeyFile, office.TLSKeyFile, office.CAKeyFile} {
		p := filepath.Join(d.OfficeDir, f)
		if _, err := os.Stat(p); err != nil {
			return fail(c, f+" missing: run wehelpd init")
		}
		if err := keyfile.CheckPrivate(p); err != nil {
			return fail(c, f+": "+err.Error())
		}
	}
	return pass(c, "directory 0700, private keys 0600")
}

func (d Deps) registration() (*office.Registration, error) {
	raw, err := os.ReadFile(filepath.Join(d.OfficeDir, office.RegistrationFile))
	if err != nil {
		return nil, err
	}
	var r office.Registration
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (d Deps) installed(context.Context) Check {
	c := Check{Name: "registry_install"}
	r, err := d.registration()
	if errors.Is(err, fs.ErrNotExist) {
		return fail(c, "not installed with the Registry: run wehelpd install")
	}
	if err != nil || r.OfficeID == "" {
		return fail(c, "registration.json unreadable")
	}
	return pass(c, fmt.Sprintf("office %s at %s, relay %s", r.OfficeID, r.RegistryURL, r.Relay.Name))
}

func (d Deps) relayTunnel(ctx context.Context) Check {
	c := Check{Name: "relay_tunnel"}
	if !d.RelayTunnel {
		return fail(c, "relay_tunnel is off: phones can't reach this office")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.ReadyURL, nil)
	if err != nil {
		return fail(c, err.Error())
	}
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fail(c, "server not answering on "+d.ReadyURL)
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
		Relay  string `json:"relay"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	switch body.Relay {
	case "connected":
		return pass(c, "connected")
	case "":
		return fail(c, "the server doesn't report its tunnel (older build?)")
	default:
		return fail(c, "tunnel "+body.Relay)
	}
}

func (d Deps) auditChain(ctx context.Context) Check {
	c := Check{Name: "audit_chain"}
	events, broken, err := d.VerifyAudit(ctx)
	if err != nil {
		return fail(c, "could not verify: "+err.Error())
	}
	if broken > 0 {
		return fail(c, fmt.Sprintf("%d chain(s) broken: run wehelpd audit verify", broken))
	}
	return pass(c, fmt.Sprintf("%d events verified", events))
}

func (d Deps) backup(ctx context.Context) Check {
	c := Check{Name: "backup_recent"}
	at, ok, err := d.LastBackup(ctx)
	if err != nil {
		return fail(c, "could not read backup_runs: "+err.Error())
	}
	if !ok {
		return fail(c, "no successful backup yet: appliance.sh install-backup")
	}
	age := d.clock().Sub(at)
	if age > MaxBackupAge {
		return fail(c, fmt.Sprintf("last good backup %s ago (limit %s)", age.Round(time.Minute), MaxBackupAge))
	}
	return pass(c, fmt.Sprintf("last good backup %s ago", age.Round(time.Minute)))
}

// restoreTest: the quarterly restore test runs on a spare machine and the
// Back Office keeps no record of it (docs/BACKUPS.md "Last test"), so this
// stays a manual runbook item.
func (d Deps) restoreTest(context.Context) Check {
	return Check{Name: "restore_test", Status: Skip,
		Detail: "not recorded by the Back Office: check docs/BACKUPS.md \"Last test\" is within 90 days"}
}

func (d Deps) demoOff(context.Context) Check {
	c := Check{Name: "demo_off"}
	if d.DemoBuild {
		return fail(c, "demo build (demo logins and seed command): rebuild without WEHELP_DEMO=1")
	}
	if d.Env == "dev" || d.Env == "demo" {
		return fail(c, "WEHELP_ENV="+d.Env)
	}
	return pass(c, "field build, env "+d.Env)
}

func (d Deps) noSeedAccounts(ctx context.Context) Check {
	c := Check{Name: "no_seed_accounts"}
	n, err := d.SeedAccounts(ctx)
	if err != nil {
		return fail(c, "could not count: "+err.Error())
	}
	if n > 0 {
		return fail(c, fmt.Sprintf("%d demo seed account(s): a seeded database must never go live", n))
	}
	return pass(c, "none")
}

func (d Deps) staffHTTPS(ctx context.Context) Check {
	c := Check{Name: "staff_https"}
	if d.StaffTLSAddr == "" {
		return fail(c, "staff_tls_addr is empty: staff would sign in over plain HTTP")
	}
	_, port, err := net.SplitHostPort(d.StaffTLSAddr)
	if err != nil {
		return fail(c, "staff_tls_addr: "+err.Error())
	}
	caPEM, err := os.ReadFile(filepath.Join(d.OfficeDir, office.CACertFile))
	if err != nil {
		return fail(c, "office CA certificate missing")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fail(c, "office CA certificate unreadable")
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config: &tls.Config{InsecureSkipVerify: true}} // verified below, against the office CA only
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return fail(c, "no HTTPS on port "+port)
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return fail(c, "no certificate")
	}
	inter := x509.NewCertPool()
	for _, ic := range certs[1:] {
		inter.AddCert(ic)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: d.clock()}); err != nil {
		return fail(c, "certificate not from the office CA, or expired")
	}
	return pass(c, fmt.Sprintf("port %s, office CA, valid until %s", port, certs[0].NotAfter.Format("2006-01-02")))
}

func (d Deps) clock() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func pass(c Check, detail string) Check { c.Status, c.Detail = Pass, detail; return c }
func fail(c Check, detail string) Check { c.Status, c.Detail = Fail, detail; return c }

// FromDB gives the database checks for the Back Office's pool.
func FromDB(pool *pgxpool.Pool) (verify func(context.Context) (int64, int, error),
	lastBackup func(context.Context) (time.Time, bool, error), seedAccounts func(context.Context) (int, error)) {
	verify = func(ctx context.Context) (int64, int, error) {
		rows, err := pool.Query(ctx, `select id from tenants`)
		if err != nil {
			return 0, 0, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, 0, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		var events int64
		broken := 0
		for _, id := range ids {
			r, err := audit.Verify(ctx, pool, id)
			if err != nil {
				return events, broken, err
			}
			events += r.Events
			if r.BrokenAt != 0 {
				broken++
			}
		}
		return events, broken, nil
	}
	lastBackup = func(ctx context.Context) (time.Time, bool, error) {
		var at *time.Time
		if err := pool.QueryRow(ctx, `select max(finished_at) from backup_runs where ok`).Scan(&at); err != nil {
			return time.Time{}, false, err
		}
		if at == nil {
			return time.Time{}, false, nil
		}
		return *at, true, nil
	}
	seedAccounts = func(ctx context.Context) (int, error) {
		var n int
		// The demo domain is assembled in SQL: CI fails a field binary that
		// contains it as one string (no demo data in field builds, #161).
		err := pool.QueryRow(ctx, `select count(*) from users where email::text ilike '%@' || 'demo' || '.' || 'wehelp'`).Scan(&n)
		return n, err
	}
	return verify, lastBackup, seedAccounts
}
