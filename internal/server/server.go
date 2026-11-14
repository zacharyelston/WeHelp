package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/office"
)

type Server struct {
	cfg       config.Config
	pool      *pgxpool.Pool
	jwtSecret []byte
	pairing   *office.Pairing // nil until the office is installed (#35)
	httpsPort string          // set: plain HTTP only redirects to HTTPS (#38)
}

func New(cfg config.Config, pool *pgxpool.Pool) *Server {
	secret := []byte(cfg.JWTSecret)
	if cfg.JWTSecret == "" {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			panic(err)
		}
		slog.Warn("WEHELP_JWT_SECRET unset; using ephemeral secret — tokens will not survive restart")
	}
	return &Server{cfg: cfg, pool: pool, jwtSecret: secret}
}

// Router builds the HTTP handler with every route. Tests use it to check the
// OpenAPI spec covers what the server actually serves.
func (s *Server) Router() chi.Router {
	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		middleware.RealIP,
		requestLogger,
		middleware.Recoverer,
		middleware.Timeout(60*time.Second),
	)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.pool.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unreachable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"message": "pong"})
		})

		r.Route("/auth", func(r chi.Router) {
			// Password routes are office-LAN only (#38); phones use pairing.
			r.With(lanOnly).Post("/register", s.Register)
			r.With(lanOnly).Post("/login", s.Login)
			r.Post("/refresh", s.Refresh)
			r.Post("/enroll", s.Enroll)
			r.Post("/logout", s.Logout)
		})

		// Staff web sign-in (#38): password + TOTP, cookie session, LAN only.
		r.Route("/staff", func(r chi.Router) {
			r.Use(lanOnly)
			r.Post("/login", s.StaffLogin)
			r.Post("/totp/setup", s.StaffTOTPSetup)
			r.Post("/totp/confirm", s.StaffTOTPConfirm)
			r.Get("/me", s.StaffMe)
			r.Post("/logout", s.StaffLogout)
		})

		// Phones pair through the Relay (#37); no session yet.
		r.Post("/pairing/redeem", s.RedeemInvite)

		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)
			r.Get("/me", s.Me)
			r.Post("/links", s.CreateLink)
			r.Get("/links", s.ListLinks)
			r.Post("/links/{userID}/accept", s.AcceptLink)
			r.Post("/links/{userID}/revoke", s.RevokeLink)
			r.Post("/messages", s.CreateMessage)
			r.Get("/messages", s.ListMessages)
			r.Post("/messages/{messageID}/read", s.MarkRead)
			r.Post("/appointments", s.CreateAppointment)
			r.Get("/appointments", s.ListAppointments)
			r.Patch("/appointments/{appointmentID}", s.UpdateAppointment)
			r.Get("/devices", s.ListMyDevices)
			r.Delete("/devices/{deviceID}", s.RevokeMyDevice)
		})

		// Staff routes: office LAN only. Through the Relay they are a 404
		// before any authentication runs (#38).
		r.Group(func(r chi.Router) {
			r.Use(lanOnly, s.authenticate)
			r.Get("/patients/{patientID}/devices", s.ListPatientDevices)
			r.Delete("/patients/{patientID}/devices/{deviceID}", s.RevokePatientDevice)
			r.Get("/patients", s.ListPatients)
			r.Post("/patients", s.CreatePatient)
			r.Post("/pairing/invites", s.CreateInvite)
			r.Get("/pairing/invites/{inviteID}", s.GetInvite)
		})
	})

	// Static web client (SPA) — served at root, falls back to API routes above.
	s.webRoutes(r)

	return r
}

// RedirectHTTPTo makes the plain-HTTP listener serve only health checks and
// redirect everything else to HTTPS on this port (#38).
func (s *Server) RedirectHTTPTo(httpsPort string) { s.httpsPort = httpsPort }

// healthOrRedirect keeps /healthz and /readyz on plain HTTP (container
// health checks) and sends everything else to the HTTPS staff listener.
func healthOrRedirect(next http.Handler, httpsPort string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		http.Redirect(w, r, "https://"+net.JoinHostPort(host, httpsPort)+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

// ServeStaffTLS serves the API and staff web UI over HTTPS on the office LAN
// with the certificate issued by the office's local CA (#38).
func (s *Server) ServeStaffTLS(ctx context.Context, addr string, cert tls.Certificate) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("wehelp staff HTTPS listening", "addr", addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	handler := http.Handler(s.Router())
	if s.httpsPort != "" {
		handler = healthOrRedirect(handler, s.httpsPort)
	}
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("wehelp listening", "addr", s.cfg.ListenAddr, "env", s.cfg.Env)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration", time.Since(start).String(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}
