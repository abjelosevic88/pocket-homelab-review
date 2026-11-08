// Package server wires the HTTP API, the live channel, auth and the embedded SPA.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/crypto/bcrypt"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-review/internal/config"
	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// Version is injected at build time.
var Version = "dev"

// Server holds dependencies.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	log      *slog.Logger
	webFS    fs.FS
	started  time.Time
	requests atomic.Int64
	errors   atomic.Int64
	hub      *hub

	oidcMu sync.Mutex
	oidc   *oidcProvider

	core    *core.Client
	coreMu  sync.Mutex
	coreOK  bool
	coreErr string
}

// New creates a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, webFS fs.FS) *Server {
	ua := "PocketReview/" + Version + " (+https://github.com/abjelosevic88/pocket-homelab-review)"
	s := &Server{cfg: cfg, store: st, log: log, webFS: webFS, started: time.Now(),
		oidc: &oidcProvider{issuerURL: cfg.OIDCIssuer},
		core: core.New(cfg.CoreURL, core.Options{DB: st.DB, TokenFile: cfg.DataDir + "/core.token", UserAgent: ua})}
	s.hub = newHub(s)
	return s
}

// Bootstrap seeds the first admin (if configured) and demo data, and starts
// the review clock.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := s.core.Start(ctx); err != nil {
		return err
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && s.cfg.AuthMode == "local" && s.cfg.AdminEmail != "" && s.cfg.AdminPassword != "" {
		if len(s.cfg.AdminPassword) < 8 {
			return errors.New("ADMIN_PASSWORD must be at least 8 characters")
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
		u := &store.User{Email: s.cfg.AdminEmail, Name: s.cfg.AdminName, PasswordHash: string(h), Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
		st, _ := s.store.GetSettings(ctx)
		st.SetupComplete = true
		if err := s.store.SaveSettings(ctx, st); err != nil {
			return err
		}
		s.log.Info("admin user created from environment", "email", u.Email)
	}
	if s.cfg.DemoData {
		if err := s.seedDemo(ctx); err != nil {
			return err
		}
	}
	_ = s.store.PurgeSessions(ctx)
	go s.hub.run(ctx)
	if s.core.Configured() {
		go s.coreLoop(ctx)
	}
	return nil
}

// ---- Pocket Core ----

// coreLoop registers with Core on start (retrying until it answers) and
// re-registers every 30 minutes so version and last-seen stay fresh.
func (s *Server) coreLoop(ctx context.Context) {
	wait := 5 * time.Second
	for {
		err := s.registerWithCore(ctx)
		s.coreMu.Lock()
		s.coreOK, s.coreErr = err == nil, ""
		if err != nil {
			s.coreErr = err.Error()
		}
		s.coreMu.Unlock()
		if err != nil {
			s.log.Warn("pocket core: registration failed, retrying", "err", err, "in", wait.String())
		} else {
			wait = 30 * time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err != nil && wait < 5*time.Minute {
			wait *= 2
		}
	}
}

func (s *Server) registerWithCore(ctx context.Context) error {
	m := core.Manifest{
		Slug: "review", Name: "Pocket Review",
		Description: "Review documents together: comments pinned to the page, issues that get resolved or rejected, tasks, sign-off, live presence and a full report of who spent how long on what.",
		Icon:        "/favicon.svg", Version: Version,
		URL: s.cfg.CoreAppURL, PublicURL: s.cfg.BaseURL, HealthPath: "/healthz", EnvPrefix: "REVIEW",
		Publishes: []string{"review.review_started", "review.comment_added", "review.review_decided"},
		Offers:    linkOffers,
	}
	// Ask Core for a relying-party client so "Sign in with Pocket Core" needs no configuration.
	if s.cfg.BaseURL != "" {
		m.OIDC = &core.OIDCRequest{RedirectURIs: []string{s.oidcCallbackURL()}}
	}
	reg, err := s.core.Register(ctx, s.cfg.CoreRegistrationKey, m)
	if err != nil {
		return err
	}
	if reg.Token != "" {
		s.log.Info("pocket core: registered, app token stored", "core", s.cfg.CoreURL)
	} else {
		s.log.Debug("pocket core: re-registered", "core", s.cfg.CoreURL)
	}
	return nil
}

// publish persists an event locally; the SDK retries network delivery.

// coreStatus summarises the Pocket Core link for the About page.
func (s *Server) coreStatus() map[string]any {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	return map[string]any{"configured": s.core.Configured(), "url": s.cfg.CoreURL, "registered": s.coreOK, "error": s.coreErr}
}

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)

	r.Get("/pocket-ask.js", s.core.AskWidget("review"))
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	r.Route("/api/v1", func(r chi.Router) {
		// Long-lived and large requests: no timeout, no compression.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.With(s.withReview).Get("/reviews/{rid}/live", s.handleLive)
			r.With(s.withReview).Post("/reviews/{rid}/revisions", s.handleUploadRevision)
			r.Post("/reviews/import", s.handleImportReview)
			r.With(s.withReview).Get("/reviews/{rid}/revisions/{vid}/file", s.handleRevisionFile)
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.Compress(5))
			r.Use(middleware.Timeout(60 * time.Second))

			r.Post("/auth/login", s.handleLogin)
			r.Post("/auth/setup", s.handleSetup)
			r.Get("/auth/status", s.handleAuthStatus)
			r.Get("/auth/oidc/start", s.handleOIDCStart)
			r.Get("/auth/oidc/callback", s.handleOIDCCallback)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth)
				r.Post("/auth/logout", s.handleLogout)
				r.Get("/auth/me", s.handleMe)
				r.Put("/auth/me", s.handleUpdateMe)

				r.Get("/dashboard", s.handleDashboard)
				r.Get("/activity", s.handleAllActivity)
				r.Get("/tasks", s.handleMyTasks)

				r.Get("/reviews", s.handleListReviews)
				r.Get("/reviews/lookup", s.handleLookup)
				r.Post("/reviews", s.handleCreateReview)
				r.Route("/reviews/{rid}", func(r chi.Router) {
					r.Use(s.withReview)
					r.Get("/", s.handleGetReview)
					r.Put("/", s.handleUpdateReview)
					r.Delete("/", s.handleDeleteReview)
					r.Post("/status", s.handleReviewStatus)
					r.Post("/verdict", s.handleVerdict)
					r.Get("/presence", s.handlePresence)

					r.Put("/revisions/{vid}/pages", s.handleSetPageCount)

					r.Post("/participants", s.handleAddParticipant)
					r.Put("/participants/{uid}", s.handleUpdateParticipant)
					r.Delete("/participants/{uid}", s.handleRemoveParticipant)

					r.Get("/comments", s.handleListComments)
					r.Post("/comments", s.handleCreateComment)
					r.Put("/comments/{cid}", s.handleEditComment)
					r.Delete("/comments/{cid}", s.handleDeleteComment)
					r.Post("/comments/{cid}/status", s.handleCommentStatus)
					r.Post("/images", s.handleUploadImage)
					r.Get("/images/{iid}", s.handleImageFile)

					r.Get("/tasks", s.handleListTasks)
					r.Post("/tasks", s.handleCreateTask)
					r.Put("/tasks/{tid}", s.handleUpdateTask)
					r.Delete("/tasks/{tid}", s.handleDeleteTask)

					r.Get("/events", s.handleReviewEvents)
					r.Get("/events.csv", s.handleEventsCSV)
					r.Get("/comments.csv", s.handleCommentsCSV)
					r.Get("/report", s.handleReport)
					r.Get("/report.pdf", s.handleReportPDF)
				})

				r.Get("/notifications", s.handleListNotifications)
				r.Get("/notifications/unread", s.handleUnreadNotifications)
				r.Post("/notifications/read", s.handleReadNotifications)
				r.Get("/notifications/settings", s.handleNotifyPrefs)
				r.Put("/notifications/settings", s.handleNotifyPrefs)
				r.Get("/push/key", s.handlePushKey)
				r.Post("/push/subscribe", s.handlePushSubscribe)
				r.Post("/push/unsubscribe", s.handlePushUnsubscribe)
				r.Post("/push/test", s.handlePushTest)

				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Put("/users/{id}", s.handleUpdateUser)
				r.Delete("/users/{id}", s.handleDeleteUser)

				r.Get("/settings", s.handleGetSettings)
				r.Put("/settings", s.handlePutSettings)
				r.Get("/settings/system", s.handleSystemInfo)

				r.Get("/tokens", s.handleListTokens)
				r.Post("/tokens", s.handleCreateToken)
				r.Delete("/tokens/{id}", s.handleDeleteToken)
			})
		})
	})

	r.NotFound(middleware.Compress(5)(http.HandlerFunc(s.handleSPA)).ServeHTTP)
	return r
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 2<<20))
	return dec.Decode(v)
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := store.ParseDate(s)
	return err == nil
}

func displayName(u *store.User) string {
	if u == nil {
		return "Someone"
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_review_info Build info.\n# TYPE pocket_review_info gauge\npocket_review_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_review_uptime_seconds Process uptime.\n# TYPE pocket_review_uptime_seconds gauge\npocket_review_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_review_http_requests_total HTTP requests served.\n# TYPE pocket_review_http_requests_total counter\npocket_review_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_review_http_errors_total HTTP 5xx responses.\n# TYPE pocket_review_http_errors_total counter\npocket_review_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_review_live_connections Open live connections.\n# TYPE pocket_review_live_connections gauge\npocket_review_live_connections %d\n", s.hub.connCount())
	fmt.Fprintf(w, "# HELP pocket_review_rows Rows per table.\n# TYPE pocket_review_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_review_rows{table=%q} %d\n", k, v)
	}
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}
