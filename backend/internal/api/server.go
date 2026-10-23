// Package api exposes the domain over a small REST API using only the standard
// library net/http ServeMux.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/garmin/workout-viewer/backend/internal/domain"
	"github.com/garmin/workout-viewer/backend/internal/importer"
	"github.com/garmin/workout-viewer/backend/internal/persistence"
)

// Server bundles the HTTP handlers with their dependencies.
type Server struct {
	repo       persistence.ActivityRepository
	importer   *importer.Service
	log        *slog.Logger
	corsConfig CORSConfig
}

// CORSConfig lists allowed origins for cross-origin requests.
type CORSConfig struct {
	Origins []string
}

// New builds a Server and its route table.
func New(repo persistence.ActivityRepository, log *slog.Logger, cors CORSConfig) *http.Server {
	s := &Server{repo: repo, log: log, corsConfig: cors}
	s.importer = importer.NewService(repo, log, 0)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("POST /api/v1/activities", s.handleUploadActivity)
	mux.HandleFunc("GET /api/v1/activities", s.handleListActivities)
	mux.HandleFunc("GET /api/v1/activities/{id}", s.handleGetActivity)
	mux.HandleFunc("GET /api/v1/activities/{id}/metrics", s.handleGetMetrics)
	mux.HandleFunc("GET /api/v1/activities/{id}/samples", s.handleGetSamples)
	mux.HandleFunc("GET /api/v1/activities/{id}/laps", s.handleGetLaps)
	mux.HandleFunc("GET /api/v1/summary", s.handleSummary)
	mux.HandleFunc("GET /api/v1/sports", s.handleSportDistribution)

	registerDocs(mux)

	return &http.Server{
		Addr:              ":8080",
		Handler:           s.withCORS(s.logging(s.recovery(mux))),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// logging records a line per request with method, path, status and duration.
func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).String())
	})
}

// recovery turns panics into a 500 so the server stays up.
func (s *Server) recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered", "error", rec)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error", "status": http.StatusInternalServerError})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// cors handles preflight requests and sets allow-origin headers for allowed
// origins. Requests without an Origin header pass through untouched.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			for _, o := range s.corsConfig.Origins {
				if o == "*" || strings.EqualFold(o, origin) {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Add("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
					w.Header().Add("Access-Control-Allow-Headers", "Content-Type")
					break
				}
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- handlers ----

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	pinger, ok := s.repo.(interface {
		Ping(ctx context.Context) error
	})
	if ok {
		if err := pinger.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database not ready", "status": http.StatusServiceUnavailable})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListActivities(w http.ResponseWriter, r *http.Request) {
	p, err := parsePagination(r.URL.Query())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.repo.ListActivities(r.Context(), f, p)
	if err != nil {
		s.log.Error("list activities", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list activities")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGetActivity(w http.ResponseWriter, r *http.Request) {
	a, err := s.repo.GetActivity(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeRepoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := s.repo.GetMetrics(r.Context(), r.PathValue("id"))
	if err != nil {
		s.log.Error("get metrics", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list metrics")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleGetSamples(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	var kp *domain.MetricKind
	if kind != "" {
		k := domain.MetricKind(kind)
		kp = &k
	}
	samples, err := s.repo.GetSamples(r.Context(), r.PathValue("id"), kp)
	if err != nil {
		s.log.Error("get samples", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list samples")
		return
	}
	writeJSON(w, http.StatusOK, samples)
}

func (s *Server) handleGetLaps(w http.ResponseWriter, r *http.Request) {
	laps, err := s.repo.GetLaps(r.Context(), r.PathValue("id"))
	if err != nil {
		s.log.Error("get laps", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list laps")
		return
	}
	writeJSON(w, http.StatusOK, laps)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	wnd, err := parseWindow(r.URL.Query())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sum, err := s.repo.Summary(r.Context(), wnd)
	if err != nil {
		s.log.Error("summary", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to compute summary")
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleSportDistribution(w http.ResponseWriter, r *http.Request) {
	dist, err := s.repo.SportDistribution(r.Context())
	if err != nil {
		s.log.Error("sport distribution", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to compute sport distribution")
		return
	}
	writeJSON(w, http.StatusOK, dist)
}

// handleUploadActivity imports a FIT/TCX/GPX workout file. The file is sent as a
// multipart form field named "file"; an optional "filename" field overrides the
// name used for format detection.
func (s *Server) handleUploadActivity(w http.ResponseWriter, r *http.Request) {
	// Cap the body before reading so a client cannot exhaust memory.
	r.Body = http.MaxBytesReader(w, r.Body, int64(importer.MaxUploadSizeBytes))

	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "provide a file in the \"file\" form field")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "file exceeds maximum upload size")
			return
		}
		s.writeError(w, http.StatusBadRequest, "failed to read uploaded file")
		return
	}

	filename := r.FormValue("filename")
	if filename == "" {
		filename = header.Filename
	}
	if filename == "" {
		filename = "activity"
	}

	res, err := s.importer.Import(data, filename)
	if err != nil {
		switch {
		case errors.Is(err, importer.ErrUnsupportedFormat):
			s.writeError(w, http.StatusUnsupportedMediaType, err.Error())
		case strings.Contains(err.Error(), "exceeds maximum upload size"):
			s.writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		default:
			// The file was accepted but could not be turned into an activity.
			s.writeError(w, http.StatusUnprocessableEntity, "failed to import file: "+err.Error())
		}
		return
	}

	// 201 when a new activity was stored, 200 when the import was a duplicate.
	status := http.StatusOK
	if !res.Duplicate {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"format":      res.Format,
		"sourceID":    res.SourceID,
		"activityID":  res.ActivityID,
		"lapCount":    res.LapCount,
		"sampleCount": res.SampleCount,
		"duplicate":   res.Duplicate,
	})
}

// docsFS embeds the OpenAPI spec and Swagger UI page so the API documentation
// is served by the standard library with no external dependencies.
//
//go:embed docs
var docsFS embed.FS

// registerDocs serves the OpenAPI spec at /docs/openapi.yaml and an interactive
// Swagger UI page at /docs, all from the embedded filesystem.
func registerDocs(mux *http.ServeMux) {
	sub, err := fs.Sub(docsFS, "docs")
	if err != nil {
		panic("embed docs: " + err.Error())
	}
	fsHandler := http.FileServer(http.FS(sub))
	// The embedded filesystem is rooted at docs/, so strip the /docs/ prefix
	// before handing the request to the file server.
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".yaml") || strings.HasSuffix(r.URL.Path, ".yml") {
			w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		}
		fsHandler.ServeHTTP(w, r)
	})))
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusFound)
	})
}

// ---- helpers ----

func (s *Server) writeRepoError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, persistence.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "activity not found")
	default:
		s.log.Error("repo error", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to load activity")
	}
}

func parsePagination(q query) (domain.Pagination, error) {
	p := domain.Pagination{Offset: 0, Limit: 25}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 200 {
			return p, errors.New("limit must be a positive integer up to 200")
		}
		p.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, errors.New("offset must be a non-negative integer")
		}
		p.Offset = n
	}
	return p, nil
}

func parseFilter(q query) (persistence.Filter, error) {
	f := persistence.Filter{}
	if v := q.Get("sport"); v != "" {
		sp := domain.Sport(v)
		f.Sport = &sp
	}
	if v := q.Get("text"); v != "" {
		f.Text = v
	}
	if v := q.Get("start"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, errors.New("start must be an RFC3339 timestamp")
		}
		f.Start = &t
	}
	if v := q.Get("end"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, errors.New("end must be an RFC3339 timestamp")
		}
		f.End = &t
	}
	return f, nil
}

func parseWindow(q query) (*persistence.Window, error) {
	w := &persistence.Window{}
	if v := q.Get("start"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, errors.New("start must be an RFC3339 timestamp")
		}
		w.Start = &t
	}
	if v := q.Get("end"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, errors.New("end must be an RFC3339 timestamp")
		}
		w.End = &t
	}
	return w, nil
}

type query interface {
	Get(string) string
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.log.Warn("error response", "status", status, "msg", msg)
	writeJSON(w, status, map[string]any{"error": msg, "status": status})
}
