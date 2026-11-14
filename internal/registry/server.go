package registry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server is the Registry's public HTTP API: install and the office list.
// The console API (#61) is not part of V1-M2; WeHelp support uses the
// wehelp-registry CLI on the Registry host instead.
type Server struct {
	store *Store
}

func NewServer(st *Store) *Server { return &Server{store: st} }

func (s *Server) Router() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, logRequests, middleware.Recoverer, middleware.Timeout(30*time.Second))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Route("/v1", func(r chi.Router) {
		r.Post("/install", s.install)
		r.Get("/office-list", s.officeList)
	})
	return r
}

// ListenAndServe serves until ctx ends. With certFile and keyFile it serves
// HTTPS; without them, plain HTTP for local development only.
func (s *Server) ListenAndServe(ctx context.Context, addr, certFile, keyFile string) error {
	srv := &http.Server{Addr: addr, Handler: s.Router(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	var err error
	if certFile != "" && keyFile != "" {
		slog.Info("wehelp-registry listening", "addr", addr, "tls", true)
		err = srv.ListenAndServeTLS(certFile, keyFile)
	} else {
		slog.Warn("wehelp-registry serving plain HTTP; set registry_tls_cert and registry_tls_key outside development", "addr", addr)
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type installRequest struct {
	InstallToken string `json:"install_token"`
	SSHPublicKey string `json:"ssh_public_key"`
}

func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	var req installRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.InstallToken == "" || req.SSHPublicKey == "" {
		writeError(w, http.StatusBadRequest, "install_token and ssh_public_key are required")
		return
	}
	res, reason, err := s.store.Install(r.Context(), req.InstallToken, req.SSHPublicKey)
	switch {
	case err == nil:
		slog.Info("office installed", "office_id", res.OfficeID, "relay_id", res.Relay.ID)
		writeJSON(w, http.StatusOK, res)
	case errors.Is(err, ErrInvalidToken):
		// One answer for every token failure; the reason stays in our logs.
		slog.Warn("install refused", "reason", reason, "request_id", middleware.GetReqID(r.Context()))
		writeError(w, http.StatusUnauthorized, "invalid install token")
	case errors.Is(err, ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrKeyInUse):
		writeError(w, http.StatusConflict, "SSH key already belongs to another office")
	default:
		slog.Error("install failed", "err", err, "request_id", middleware.GetReqID(r.Context()))
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) officeList(w http.ResponseWriter, r *http.Request) {
	env, err := s.store.LatestList(r.Context())
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusServiceUnavailable, "no office list published yet")
		return
	}
	if err != nil {
		slog.Error("office list", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, env)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// logRequests logs method, path and status only: no bodies, tokens or keys.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"duration", time.Since(start).String(), "request_id", middleware.GetReqID(r.Context()))
	})
}
