package seed

// The training profile (#80): a synthetic week at a naturopath office, for
// staff onboarding and the training videos. It adds to Demo Clinic, so the
// demo logins, pairing and the practice website keep working:
//
//   - 3 providers, a front desk and an admin, all with TrainingPassword;
//   - 25 obviously fictional patients at @demo.wehelp (no phone numbers:
//     WeHelp stores none), linked round-robin
//     to the providers (most active, a few pending);
//   - Monday to Friday of the current week: completed, no-show and
//     cancelled visits on past days, scheduled and cancelled ones ahead;
//   - appointment requests, pending and declined;
//   - message threads that are unread, awaiting a reply, or answered.
//
// Layout comes from a fixed random seed, so screenshots match run to run in
// the same week. Like Run it is idempotent: a second run in the same week
// writes nothing and records no audit events. Every write is audited in the
// same transaction, as real data is. There is no reset: audit events can't
// be deleted, so starting over means a fresh demo database.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// TrainingPassword is every training staff account's password. Staff still
// set up an authenticator at their first sign-in.
const TrainingPassword = "wehelp-training-staff"

// trainingSeed fixes the layout: names, links, schedule and threads.
const trainingSeed = 80

var trainingStaff = []struct{ email, name, role string }{
	{"dr.mira.okafor@demo.wehelp", "Dr. Mira Okafor, ND", "provider"},
	{"dr.tomas.varga@demo.wehelp", "Dr. Tomas Varga, ND", "provider"},
	{"dr.lena.hart@demo.wehelp", "Dr. Lena Hart, ND", "provider"},
	{"training.frontdesk@demo.wehelp", "Training Front Desk", "office"},
	{"training.admin@demo.wehelp", "Training Admin", "admin"},
}

// Obviously fictional: plants and birds, no real person's name.
var (
	trainingFirst = []string{"Juniper", "Rowan", "Sorrel", "Linden", "Wren", "Hazel", "Basil", "Fern",
		"Aspen", "Clover", "Sage", "Briar", "Marigold", "Thistle", "Laurel", "Alder"}
	trainingLast = []string{"Finchley", "Sparrowhawk", "Bramblewood", "Kestrel", "Teasel", "Plover",
		"Nightjar", "Larkspur", "Mossgrove", "Dunnock", "Goldcrest", "Pipit"}
)

const trainingPatients = 25

// TrainingResult counts what a run created.
type TrainingResult struct {
	TenantID     uuid.UUID
	Staff        []Account
	Patients     []Account
	Appointments int
	Requests     int
	Messages     int
	WeekOf       time.Time // Monday 00:00 in the office's time zone
}

type trainingPerson struct {
	acct     *Account
	name     string
	provider int // index into providers
	active   bool
}

// RunTraining adds the training week to Demo Clinic (running the default
// seed first if needed). now and loc place the week: loc is the office's
// time zone (the timezone setting), or time.Local.
func RunTraining(ctx context.Context, pool *pgxpool.Pool, out io.Writer, now time.Time, loc *time.Location) (*TrainingResult, error) {
	if _, err := Run(ctx, pool, io.Discard); err != nil {
		return nil, err
	}
	if loc == nil {
		loc = time.Local
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("seed: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx, `select id from tenants where name = $1`, DemoTenant).Scan(&tenantID); err != nil {
		return nil, fmt.Errorf("seed: tenant: %w", err)
	}
	res := &TrainingResult{TenantID: tenantID, WeekOf: mondayOf(now.In(loc))}
	rng := rand.New(rand.NewPCG(trainingSeed, trainingSeed))

	var providers []*Account
	for _, s := range trainingStaff {
		a, err := ensureUser(ctx, tx, tenantID, s.email, TrainingPassword, s.role, s.name)
		if err != nil {
			return nil, err
		}
		res.Staff = append(res.Staff, *a)
		if s.role == "provider" {
			providers = append(providers, a)
		}
	}

	// Patients: unique first/last pairs in a fixed shuffled order.
	pairs := make([][2]string, 0, len(trainingFirst)*len(trainingLast))
	for _, f := range trainingFirst {
		for _, l := range trainingLast {
			pairs = append(pairs, [2]string{f, l})
		}
	}
	rng.Shuffle(len(pairs), func(i, j int) { pairs[i], pairs[j] = pairs[j], pairs[i] })
	var people []trainingPerson
	for i := 0; i < trainingPatients; i++ {
		name := pairs[i][0] + " " + pairs[i][1]
		email := strings.ToLower(pairs[i][0]+"."+pairs[i][1]) + "@demo.wehelp"
		a, err := ensureUser(ctx, tx, tenantID, email, "", "patient", name)
		if err != nil {
			return nil, err
		}
		res.Patients = append(res.Patients, *a)
		// Every eighth patient's invite is still pending.
		p := trainingPerson{acct: a, name: name, provider: i % len(providers), active: i%8 != 7}
		people = append(people, p)
		want, action := "active", "link.accept"
		if !p.active {
			want, action = "pending", ""
		}
		if _, err := ensureLink(ctx, tx, tenantID, providers[p.provider].ID, a.ID, want, action); err != nil {
			return nil, err
		}
	}

	var active []trainingPerson
	for _, p := range people {
		if p.active {
			active = append(active, p)
		}
	}

	// The week: each provider sees 3 or 4 patients a day, Monday to Friday,
	// on the hour from 9:00 to 15:00, so no two visits overlap.
	for day := 0; day < 5; day++ {
		date := res.WeekOf.AddDate(0, 0, day)
		for pi, prov := range providers {
			mine := patientsOf(active, pi)
			slots := rng.Perm(7)[:3+rng.IntN(2)]
			for k, slot := range slots {
				p := mine[(day*4+k)%len(mine)]
				start := date.Add(time.Duration(9+slot) * time.Hour)
				length := 30 * time.Minute
				if rng.IntN(3) == 0 {
					length = 45 * time.Minute
				}
				status := trainingStatus(rng, start, now)
				ok, err := ensureTrainingAppointment(ctx, tx, tenantID, prov.ID, p.acct.ID, start, start.Add(length), status)
				if err != nil {
					return nil, err
				}
				if ok {
					res.Appointments++
				}
			}
		}
	}

	// Requests for later this week or next: four pending, one declined.
	for i := 0; i < 5; i++ {
		p := active[(i*5+2)%len(active)]
		day := res.WeekOf.AddDate(0, 0, 3+i)
		status, note := "pending", ""
		if i == 4 {
			status, note = "declined", "Fully booked that week; please call the front desk."
		}
		ok, err := ensureTrainingRequest(ctx, tx, tenantID, providers[p.provider].ID, p.acct.ID, day,
			[]string{"morning", "afternoon", "any"}[i%3], trainingReasons[i], status, note)
		if err != nil {
			return nil, err
		}
		if ok {
			res.Requests++
		}
	}

	// Threads: unread (the patient wrote, nobody has read it), awaiting a
	// reply (read, not answered), and answered.
	for i, p := range active[:12] {
		prov := providers[p.provider]
		first := strings.Fields(p.name)[0]
		var msgs []threadMsg
		switch i % 3 {
		case 0:
			msgs = []threadMsg{{p.acct.ID, prov.ID, trainingQuestions[i%len(trainingQuestions)], false}}
		case 1:
			msgs = []threadMsg{{p.acct.ID, prov.ID, trainingQuestions[i%len(trainingQuestions)], true}}
		default:
			msgs = []threadMsg{
				{p.acct.ID, prov.ID, trainingQuestions[i%len(trainingQuestions)], true},
				{prov.ID, p.acct.ID, "Thanks, " + first + ". Let's go over it at your next visit; message me if anything changes.", false},
			}
		}
		n, err := ensureThread(ctx, tx, tenantID, msgs, now)
		if err != nil {
			return nil, err
		}
		res.Messages += n
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("seed: commit: %w", err)
	}
	printTraining(out, res)
	return res, nil
}

var trainingReasons = []string{
	"Follow-up on my sleep plan",
	"New patient intake",
	"Question about my supplements",
	"Annual review",
	"Digestive symptoms came back",
}

var trainingQuestions = []string{
	"Should I keep taking the magnesium at night or switch to mornings?",
	"My energy has been better this week. Is it OK to reduce the dose?",
	"Can I move Thursday's appointment to the afternoon?",
	"I logged my weigh-ins. Do you need anything else before my visit?",
	"Is it normal to feel a bit tired in the first week of the new plan?",
	"Could you send me the name of the herbal tea you mentioned?",
}

func mondayOf(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	offset := (int(d.Weekday()) + 6) % 7 // Monday = 0
	return d.AddDate(0, 0, -offset)
}

func patientsOf(people []trainingPerson, provider int) []trainingPerson {
	var out []trainingPerson
	for _, p := range people {
		if p.provider == provider {
			out = append(out, p)
		}
	}
	return out
}

// trainingStatus: past visits mostly completed, some no-shows and
// cancellations; future ones scheduled, a few cancelled.
func trainingStatus(rng *rand.Rand, start, now time.Time) string {
	r := rng.IntN(10)
	if start.Before(now) {
		switch {
		case r == 0:
			return "no_show"
		case r == 1:
			return "cancelled"
		default:
			return "completed"
		}
	}
	if r == 0 {
		return "cancelled"
	}
	return "scheduled"
}

func ensureTrainingAppointment(ctx context.Context, tx pgx.Tx, tenantID, providerID, patientID uuid.UUID,
	start, end time.Time, status string) (bool, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		insert into appointments (tenant_id, provider_id, patient_id, starts_at, ends_at, status)
		select $1, $2, $3, $4, $5, $6
		where not exists (select 1 from appointments where tenant_id = $1 and provider_id = $2 and starts_at = $4)
		returning id`, tenantID, providerID, patientID, start, end, status).Scan(&id)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("seed: training appointment: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{"patient_id": patientID.String(), "provider_id": providerID.String(),
		"starts_at": start.UTC().Format(time.RFC3339), "status": status, "seed": "training"})
	if err := audit.Record(ctx, tx, audit.Event{TenantID: tenantID, ActorID: &providerID,
		Action: "appointment.create", ResourceType: "appointment", ResourceID: id.String(), Detail: detail}); err != nil {
		return false, fmt.Errorf("seed: audit appointment: %w", err)
	}
	return true, nil
}

func ensureTrainingRequest(ctx context.Context, tx pgx.Tx, tenantID, providerID, patientID uuid.UUID,
	day time.Time, period, reason, status, note string) (bool, error) {
	preferred, _ := json.Marshal([]map[string]string{{"date": day.Format("2006-01-02"), "period": period}})
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		insert into appointment_requests (tenant_id, patient_id, provider_id, preferred, reason, status, note, decided_at)
		select $1, $2, $3, $4, $5, $6, $7, case when $6 = 'pending' then null else now() end
		where not exists (select 1 from appointment_requests
			where tenant_id = $1 and patient_id = $2 and provider_id = $3 and preferred = $4::jsonb)
		returning id`, tenantID, patientID, providerID, preferred, reason, status, note).Scan(&id)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("seed: training request: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: tenantID, ActorID: &patientID,
		Action: "appointment_request.create", ResourceType: "appointment_request", ResourceID: id.String(),
		Detail: json.RawMessage(`{"seed":"training"}`)}); err != nil {
		return false, fmt.Errorf("seed: audit request: %w", err)
	}
	return true, nil
}

type threadMsg struct {
	from, to uuid.UUID
	body     string
	read     bool
}

// ensureThread adds the messages that aren't there yet, oldest first, a few
// hours apart and ending an hour before now.
func ensureThread(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, msgs []threadMsg, now time.Time) (int, error) {
	n := 0
	for i, m := range msgs {
		at := now.Add(-time.Duration(len(msgs)-i)*3*time.Hour + 2*time.Hour)
		var readAt *time.Time
		if m.read {
			r := at.Add(30 * time.Minute)
			readAt = &r
		}
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			insert into messages (tenant_id, sender_id, recipient_id, kind, body, read_at, created_at)
			select $1, $2, $3, 'message', $4, $5, $6
			where not exists (select 1 from messages
				where tenant_id = $1 and sender_id = $2 and recipient_id = $3 and body = $4)
			returning id`, tenantID, m.from, m.to, m.body, readAt, at).Scan(&id)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("seed: training message: %w", err)
		}
		if err := audit.Record(ctx, tx, audit.Event{TenantID: tenantID, ActorID: &m.from,
			Action: "message.create", ResourceType: "message", ResourceID: id.String(),
			Detail: json.RawMessage(`{"kind":"message","seed":"training"}`)}); err != nil {
			return n, fmt.Errorf("seed: audit message: %w", err)
		}
		n++
	}
	return n, nil
}

func printTraining(out io.Writer, r *TrainingResult) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "=== WeHelp training seed ===")
	fmt.Fprintln(out, "Practice:", DemoTenant, "  week of", r.WeekOf.Format("Mon 2 Jan 2006"))
	fmt.Fprintln(out, "Staff (password "+TrainingPassword+"; set up an authenticator at first sign-in):")
	for _, s := range trainingStaff {
		fmt.Fprintf(out, "  %-9s %-22s %s\n", s.role, s.name, s.email)
	}
	fmt.Fprintf(out, "Created this run: %d patients of %d, %d appointments, %d requests, %d messages\n",
		countCreated(r.Patients), len(r.Patients), r.Appointments, r.Requests, r.Messages)
	fmt.Fprintln(out, "=== end training seed ===")
}

func countCreated(as []Account) int {
	n := 0
	for _, a := range as {
		if a.Created {
			n++
		}
	}
	return n
}
