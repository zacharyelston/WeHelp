package registry

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// The Registry console (#61): a staff web app at /console/ and its API
// under /v1/console. support runs practices, providers and install tokens;
// registry-admin also suspends, reactivates and changes Relays. Nothing
// here can reach a Back Office or any patient data.

//go:embed console
var consoleFiles embed.FS

func (s *Server) consoleRoutes(r chi.Router) {
	r.Post("/login", s.consoleLogin)
	r.Post("/totp/setup", s.consoleTOTPSetup)
	r.Post("/totp/confirm", s.consoleTOTPConfirm)
	r.Get("/me", s.consoleMe)
	r.Post("/logout", s.consoleLogout)

	r.Group(func(r chi.Router) {
		r.Use(s.signedIn(""))
		r.Get("/practices", s.listPractices)
		r.Post("/practices", s.createPractice)
		r.Get("/practices/{id}", s.getPractice)
		r.Patch("/practices/{id}", s.updatePractice)
		r.Post("/practices/{id}/install-token", s.issueToken)
		r.Post("/practices/{id}/providers", s.addProvider)
		r.Put("/providers/{id}", s.updateProvider)
		r.Delete("/providers/{id}", s.removeProvider)
		r.Get("/relays", s.listRelays)
		r.Get("/health", s.health)
		r.Get("/audit", s.audit)
	})
	r.Group(func(r chi.Router) {
		r.Use(s.signedIn(RoleAdmin))
		r.Post("/practices/{id}/suspend", s.setStatus("suspended"))
		r.Post("/practices/{id}/reactivate", s.setStatus("active"))
		r.Post("/practices/{id}/relay", s.assignRelay)
		r.Put("/relays/{id}/capacity", s.setCapacity)
	})
}

// consoleUI serves the static console with a strict content security policy.
func consoleUI() http.Handler {
	sub, _ := fs.Sub(consoleFiles, "console")
	files := http.StripPrefix("/console/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
}

func actorOf(r *http.Request) string { return "console:" + sessionFrom(r.Context()).Email }

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
	}
	return id, err == nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return false
	}
	return true
}

// storeError maps store errors to HTTP answers.
func storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "registry: invalid input: "))
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrNoChange):
		writeError(w, http.StatusConflict, "already in that state")
	case errors.Is(err, ErrDuplicate):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, ErrInstalled):
		writeError(w, http.StatusConflict, "the office is installed; moving it to another Relay isn't supported yet")
	case errors.Is(err, ErrNotActive):
		writeError(w, http.StatusConflict, "the practice is suspended")
	default:
		slog.Error("console", "err", err, "request_id", middleware.GetReqID(r.Context()))
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) listPractices(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListPracticeDetails(r.Context())
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"practices": list})
}

func (s *Server) createPractice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Relay        string `json:"relay"`
		ContactName  string `json:"contact_name"`
		ContactEmail string `json:"contact_email"`
		ContactPhone string `json:"contact_phone"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := checkEmail("contact email", strings.TrimSpace(req.ContactEmail)); err != nil {
		storeError(w, r, err)
		return
	}
	p, err := s.store.AddPractice(r.Context(), actorOf(r), req.Name, req.Relay)
	if err != nil {
		storeError(w, r, err)
		return
	}
	if req.ContactName != "" || req.ContactEmail != "" || req.ContactPhone != "" {
		if err := s.store.UpdatePractice(r.Context(), actorOf(r), p.ID, PracticeUpdate{
			ContactName: &req.ContactName, ContactEmail: &req.ContactEmail, ContactPhone: &req.ContactPhone}); err != nil {
			storeError(w, r, err)
			return
		}
	}
	d, err := s.store.GetPractice(r.Context(), p.ID)
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) getPractice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.store.GetPractice(r.Context(), id)
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) updatePractice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var u PracticeUpdate
	if !decode(w, r, &u) {
		return
	}
	if err := s.store.UpdatePractice(r.Context(), actorOf(r), id, u); err != nil {
		storeError(w, r, err)
		return
	}
	s.getPractice(w, r)
}

func (s *Server) setStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := s.store.SetPracticeStatus(r.Context(), actorOf(r), id, status); err != nil {
			storeError(w, r, err)
			return
		}
		s.getPractice(w, r)
	}
}

func (s *Server) assignRelay(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		RelayID uuid.UUID `json:"relay_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.AssignRelay(r.Context(), actorOf(r), id, req.RelayID); err != nil {
		storeError(w, r, err)
		return
	}
	s.getPractice(w, r)
}

func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	token, expires, err := s.store.IssueInstallToken(r.Context(), actorOf(r), id)
	if err != nil {
		storeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"install_token": token, "expires_at": expires})
}

type providerBody struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	Credentials   string `json:"credentials"`
	LicenceNumber string `json:"licence_number"`
}

func (b providerBody) provider() Provider {
	return Provider{Name: b.Name, Email: b.Email, Role: b.Role, Credentials: b.Credentials, LicenceNumber: b.LicenceNumber}
}

func (s *Server) addProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b providerBody
	if !decode(w, r, &b) {
		return
	}
	p := b.provider()
	p.PracticeID = id
	p, err := s.store.AddProvider(r.Context(), actorOf(r), p)
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b providerBody
	if !decode(w, r, &b) {
		return
	}
	p := b.provider()
	p.ID = id
	p, err := s.store.UpdateProvider(r.Context(), actorOf(r), p)
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) removeProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.RemoveProvider(r.Context(), actorOf(r), id); err != nil {
		storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRelays(w http.ResponseWriter, r *http.Request) {
	h, err := s.store.Health(r.Context())
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": h.Relays})
}

func (s *Server) setCapacity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Capacity int `json:"capacity"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.SetRelayCapacity(r.Context(), actorOf(r), id, req.Capacity); err != nil {
		storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h, err := s.store.Health(r.Context())
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "limit must be 1-500")
			return
		}
		limit = n
	}
	list, err := s.store.RecentAudit(r.Context(), limit)
	if err != nil {
		storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": list})
}

// RelaySignatureHeader carries the Relay's SSH signature over the report
// body: base64 of ssh.Marshal(signature).
const RelaySignatureHeader = "X-WeHelp-Relay-Signature"

// relayStatus accepts a Relay's signed status report (#61).
func (s *Server) relayStatus(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(r.Header.Get(RelaySignatureHeader))
	sig := new(ssh.Signature)
	if err != nil || ssh.Unmarshal(raw, sig) != nil {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	switch err := s.store.AcceptRelayReport(r.Context(), body, sig); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "invalid signature")
	case errors.Is(err, ErrInvalidInput):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "registry: invalid input: "))
	default:
		slog.Error("relay status", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
