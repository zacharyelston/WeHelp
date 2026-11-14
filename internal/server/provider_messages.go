package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Provider message modes (#109). Each provider chooses how they take
// messages from patients:
//   - on: as before;
//   - hours: messages arrive at any time, but outside office hours the
//     patient gets an automatic reply (at most once every autoReplyGap)
//     and the provider gets no push;
//   - off: patients can't send them messages.
// Staff can always write to their patients. Office time is s.location().

const autoReplyGap = 12 * time.Hour

type messageSettings struct {
	Mode        string `json:"mode"` // on, hours, off
	Days        []int  `json:"days"` // 0 = Sunday
	StartMinute int    `json:"start_minute"`
	EndMinute   int    `json:"end_minute"`
	AutoReply   string `json:"auto_reply"` // empty = a standard reply
}

func defaultMessageSettings() messageSettings {
	return messageSettings{Mode: "on", Days: []int{1, 2, 3, 4, 5}, StartMinute: 9 * 60, EndMinute: 17 * 60}
}

func loadMessageSettings(ctx context.Context, q rowQuerier, userID uuid.UUID) (messageSettings, error) {
	m := defaultMessageSettings()
	var days []int32
	err := q.QueryRow(ctx, `select mode, days, start_minute, end_minute, auto_reply from provider_message_settings where user_id = $1`, userID).
		Scan(&m.Mode, &days, &m.StartMinute, &m.EndMinute, &m.AutoReply)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, nil
	}
	m.Days = make([]int, len(days))
	for i, d := range days {
		m.Days[i] = int(d)
	}
	return m, err
}

// inHours reports whether t (any zone) falls in the settings' office hours.
func (m messageSettings) inHours(t time.Time, loc *time.Location) bool {
	t = t.In(loc)
	day := int(t.Weekday())
	minute := t.Hour()*60 + t.Minute()
	for _, d := range m.Days {
		if d == day {
			return minute >= m.StartMinute && minute < m.EndMinute
		}
	}
	return false
}

// hoursText describes the hours, e.g. "Mon to Fri, 9:00 to 17:00".
func (m messageSettings) hoursText() string {
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	days := append([]int(nil), m.Days...)
	sort.Ints(days)
	var d string
	switch {
	case len(days) == 5 && days[0] == 1 && days[4] == 5:
		d = "Mon to Fri"
	case len(days) == 7:
		d = "every day"
	default:
		parts := make([]string, len(days))
		for i, x := range days {
			parts[i] = names[x]
		}
		d = strings.Join(parts, ", ")
	}
	hm := func(n int) string { return fmt.Sprintf("%d:%02d", n/60, n%60) }
	return fmt.Sprintf("%s, %s to %s", d, hm(m.StartMinute), hm(m.EndMinute))
}

// GetMessageSettings returns the signed-in staff member's message mode.
// Route: GET /api/v1/staff/profile/messages
func (s *Server) GetMessageSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	m, err := loadMessageSettings(r.Context(), s.pool, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// PutMessageSettings sets the signed-in staff member's own message mode.
// Audited as staff.message_settings.
// Route: PUT /api/v1/staff/profile/messages
func (s *Server) PutMessageSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var m messageSettings
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	m.AutoReply = strings.TrimSpace(m.AutoReply)
	seen := map[int]bool{}
	for _, d := range m.Days {
		if d < 0 || d > 6 || seen[d] {
			writeErr(w, http.StatusBadRequest, "days are 0 (Sunday) to 6, each once")
			return
		}
		seen[d] = true
	}
	switch {
	case m.Mode != "on" && m.Mode != "hours" && m.Mode != "off":
		writeErr(w, http.StatusBadRequest, "mode must be on, hours or off")
		return
	case m.Mode == "hours" && len(m.Days) == 0:
		writeErr(w, http.StatusBadRequest, "pick at least one day")
		return
	case m.StartMinute < 0 || m.EndMinute > 1440 || m.EndMinute <= m.StartMinute:
		writeErr(w, http.StatusBadRequest, "office hours must end after they start")
		return
	case utf8.RuneCountInString(m.AutoReply) > 500:
		writeErr(w, http.StatusBadRequest, "the automatic reply is limited to 500 characters")
		return
	}
	if m.Days == nil {
		m.Days = []int{}
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		insert into provider_message_settings (user_id, tenant_id, mode, days, start_minute, end_minute, auto_reply)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (user_id) do update set mode = excluded.mode, days = excluded.days, start_minute = excluded.start_minute,
			end_minute = excluded.end_minute, auto_reply = excluded.auto_reply, updated_at = now()`,
		id.UserID, id.TenantID, m.Mode, int32s(m.Days), m.StartMinute, m.EndMinute, m.AutoReply); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "staff.message_settings",
		ResourceType: "user", ResourceID: id.UserID.String(), Detail: mustJSON(map[string]string{"mode": m.Mode})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func int32s(v []int) []int32 {
	out := make([]int32, len(v))
	for i, x := range v {
		out[i] = int32(x)
	}
	return out
}

// recipientMode applies a provider's message mode to a patient's message,
// inside the send transaction. It returns whether to push to the provider
// and an automatic reply to send (empty for none), or writes a 409 and
// returns ok=false when the provider has messages off.
func (s *Server) recipientMode(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, tenant, patient, provider uuid.UUID, now time.Time) (push bool, reply string, ok bool) {
	var name, role string
	if err := tx.QueryRow(ctx, `select display_name, role::text from users where id = $1 and tenant_id = $2`, provider, tenant).
		Scan(&name, &role); err != nil || !isStaffRole(role) {
		return true, "", err == nil
	}
	m, err := loadMessageSettings(ctx, tx, provider)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "settings lookup failed")
		return false, "", false
	}
	switch m.Mode {
	case "off":
		writeErr(w, http.StatusConflict, name+" isn't taking messages in the app. Please message the practice instead.")
		return false, "", false
	case "hours":
		if m.inHours(now, s.location()) {
			return true, "", true
		}
		var recent bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from messages where sender_id = $1 and recipient_id = $2
			and metadata->>'auto_reply' = 'true' and created_at > $3)`, provider, patient, now.Add(-autoReplyGap)).Scan(&recent); err != nil {
			writeErr(w, http.StatusInternalServerError, "lookup failed")
			return false, "", false
		}
		if recent {
			return false, "", true
		}
		reply = m.AutoReply
		if reply == "" {
			reply = fmt.Sprintf("Thanks for your message. %s replies during office hours (%s).", name, m.hoursText())
		}
		return false, reply, true
	}
	return true, "", true
}
