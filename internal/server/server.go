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
	pusher    Pusher          // push through the Relay (#7); nil = off
	sitePub   SitePublisher   // website publishing through the Relay (#97); nil = off
	roster    RosterSource    // provider roster from the Registry (#62); nil = not installed
	turnKeys  TURNKeySource   // video-visit TURN key from the Relay (#6); nil = not installed
	visits    *visitHub       // video-visit signaling, in memory (#6)
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
	return &Server{cfg: cfg, pool: pool, jwtSecret: secret, visits: newVisitHub()}
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
			// Provider accounts (#100): admins invite, disable and enable;
			// the provider redeems the set-up link.
			r.Post("/setup", s.StaffSetup)
			r.With(s.authenticate).Get("/providers", s.ListProviders)
			r.With(s.authenticate).Post("/providers", s.CreateProvider)
			r.With(s.authenticate).Post("/providers/{userID}/invite", s.ReissueProviderInvite)
			r.With(s.authenticate).Post("/providers/{userID}/disable", s.DisableProvider)
			r.With(s.authenticate).Post("/providers/{userID}/enable", s.EnableProvider)
			// Provider roster from the Registry (#62): admins only.
			r.With(s.authenticate).Get("/roster", s.GetRoster)
			r.With(s.authenticate).Post("/roster/sync", s.SyncRoster)
			r.With(s.authenticate).Post("/roster/{registryID}/invite", s.InviteFromRoster)
			// Audit chain check (#24): admins, their own practice.
			r.With(s.authenticate).Get("/audit/verify", s.VerifyAuditChain)
			// Backups (#41): recent runs and a warning, admins only.
			r.With(s.authenticate).Get("/backups", s.GetBackups)
			// Your own profile (#102), used for the website bios.
			r.With(s.authenticate).Get("/profile", s.GetStaffProfile)
			r.With(s.authenticate).Put("/profile", s.PutStaffProfile)
			r.With(s.authenticate).Get("/profile/photo", s.GetStaffProfilePhoto)
			r.With(s.authenticate).Put("/profile/photo", s.PutStaffProfilePhoto)
			r.With(s.authenticate).Delete("/profile/photo", s.DeleteStaffProfilePhoto)
			r.With(s.authenticate).Get("/profile/messages", s.GetMessageSettings) // message mode (#109)
			r.With(s.authenticate).Put("/profile/messages", s.PutMessageSettings)
		})

		// Phones pair through the Relay (#37); no session yet.
		r.Post("/pairing/redeem", s.RedeemInvite)

		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)
			r.Get("/me", s.Me)
			r.Get("/practice", s.GetPractice) // practice type and words (#108)
			// Practice inbox (#110): patients any channel; the front desk on the LAN.
			r.With(s.auditRead("practice_inbox.list", "practice", "")).Get("/practice-inbox", s.GetPracticeInbox)
			r.With(s.auditRead("practice_inbox.read", "user", "patientID")).Get("/practice-inbox/{patientID}", s.GetPracticeInboxThread)
			r.Post("/practice-inbox", s.PostPracticeInbox)
			r.Post("/practice-inbox/read", s.ReadPracticeInbox)
			// Care teams (#111): the patient and their providers, any channel.
			r.With(s.auditRead("care_team.list", "practice", "")).Get("/care-teams", s.ListCareTeams)
			r.With(s.auditRead("care_team.view", "user", "patientID")).Get("/care-teams/{patientID}", s.GetCareTeam)
			r.Post("/care-teams/{patientID}/members", s.AddCareTeamMember)
			r.Post("/care-teams/{patientID}/members/{memberID}/approve", s.ApproveCareTeamMember)
			r.Post("/care-teams/{patientID}/members/{memberID}/decline", s.DeclineCareTeamMember)
			r.Delete("/care-teams/{patientID}/members/{memberID}", s.RemoveCareTeamMember)
			r.Get("/care-teams/{patientID}/messages", s.GetCareTeamMessages)
			r.Post("/care-teams/{patientID}/messages", s.PostCareTeamMessage)
			r.Post("/links", s.CreateLink)
			r.With(s.auditRead("links.list", "practice", "")).Get("/links", s.ListLinks)
			r.Post("/links/{userID}/accept", s.AcceptLink)
			r.Post("/links/{userID}/revoke", s.RevokeLink)
			r.Put("/links/{userID}/checkin", s.SetCheckinCadence)
			r.Post("/messages", s.CreateMessage)
			r.With(s.auditReadUnless("messages.list", "practice", "", threadRead)).Get("/messages", s.ListMessages)
			r.With(s.auditRead("conversations.list", "practice", "")).Get("/conversations", s.ListConversations)
			r.Post("/messages/{messageID}/read", s.MarkRead)
			r.Post("/appointments", s.CreateAppointment)
			r.With(s.auditRead("appointments.list", "practice", "")).Get("/appointments", s.ListAppointments)
			r.Patch("/appointments/{appointmentID}", s.UpdateAppointment)
			// Video visits (#6): participants, from the app.
			r.Post("/appointments/{appointmentID}/visit/start", s.StartVisit)
			r.Post("/appointments/{appointmentID}/visit/join", s.JoinVisit)
			r.Get("/visits/{visitID}/signals", s.PollVisitSignals)
			r.Post("/visits/{visitID}/signals", s.SendVisitSignal)
			r.Post("/visits/{visitID}/leave", s.LeaveVisit)
			r.Post("/visits/{visitID}/end", s.EndVisit)
			r.Post("/appointment-requests", s.CreateAppointmentRequest)
			r.With(s.auditRead("appointment_requests.list", "practice", "")).Get("/appointment-requests", s.ListAppointmentRequests)
			r.Post("/appointment-requests/{requestID}/cancel", s.CancelAppointmentRequest)
			r.Post("/appointment-requests/{requestID}/confirm", s.ConfirmAppointmentRequest)
			r.Post("/appointment-requests/{requestID}/decline", s.DeclineAppointmentRequest)
			r.Get("/devices", s.ListMyDevices)
			r.Delete("/devices/{deviceID}", s.RevokeMyDevice)
			r.Put("/devices/current/push", s.SetPushToken)
			r.Delete("/devices/current/push", s.ClearPushToken)
			// Supply store and library (#97). Staff listing orders is LAN only.
			r.Get("/store/products", s.ListProducts)
			r.Post("/orders", s.CreateOrder)
			r.With(s.auditRead("orders.list", "practice", "")).Get("/orders", s.ListOrders)
			r.Post("/orders/{orderID}/cancel", s.CancelOrder)
			r.Get("/library", s.GetLibrary)
			r.Get("/library/files/{fileID}", s.GetLibraryFile)
		})

		// Staff routes: office LAN only. Through the Relay they are a 404
		// before any authentication runs (#38).
		r.Group(func(r chi.Router) {
			r.Use(lanOnly, s.authenticate)
			r.With(s.auditRead("patient_devices.list", "user", "patientID")).Get("/patients/{patientID}/devices", s.ListPatientDevices)
			r.Delete("/patients/{patientID}/devices/{deviceID}", s.RevokePatientDevice)
			r.With(s.auditRead("patients.list", "practice", "")).Get("/patients", s.ListPatients)
			r.Post("/patients", s.CreatePatient)
			r.Post("/pairing/invites", s.CreateInvite)
			r.With(s.auditRead("pairing_invite.read", "pairing_invite", "inviteID")).Get("/pairing/invites/{inviteID}", s.GetInvite)
			r.Put("/practice", s.PutPractice)                  // admins (#108)
			r.Put("/practice/features", s.PutPracticeFeatures) // admins (#109)
			// Practice website and store admin (#97).
			r.Get("/site", s.GetSite)
			r.Put("/site", s.PutSite)
			r.Post("/site/files", s.UploadSiteFile)
			r.Delete("/site/files/{fileID}", s.DeleteSiteFile)
			r.Post("/site/publish", s.PublishSite)
			// The blog (#121).
			r.Get("/site/posts", s.ListPosts)
			r.Post("/site/posts", s.CreatePost)
			r.Put("/site/posts/{postID}", s.UpdatePost)
			r.Delete("/site/posts/{postID}", s.DeletePost)
			r.Post("/store/products", s.CreateProduct)
			r.Put("/store/products/{productID}", s.UpdateProduct)
			r.Post("/store/products/demo", s.AddDemoProducts)
			r.Post("/orders/{orderID}/status", s.SetOrderStatus)
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
