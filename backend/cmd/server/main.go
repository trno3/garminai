// Command server runs the workout viewer REST API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/garmin/workout-viewer/backend/internal/api"
	"github.com/garmin/workout-viewer/backend/internal/persistence"
)

type config struct {
	dsn         string
	httpAddr    string
	logLevel    slog.Level
	corsOrigins []string
}

func loadConfig() config {
	return config{
		dsn:         env("DATABASE_URL", "postgres://postgres:devpassword@localhost:5432/appdb?sslmode=disable"),
		httpAddr:    env("HTTP_ADDR", ":8080"),
		logLevel:    parseLevel(env("LOG_LEVEL", "info")),
		corsOrigins: splitList(env("CORS_ORIGINS", "*")),
	}
}

func main() {
	cfg := loadConfig()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	logger.Info("starting server", "addr", cfg.httpAddr)

	pool, err := pgxpool.New(context.Background(), cfg.dsn)
	if err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := persistence.Migrate(context.Background(), pool); err != nil {
		logger.Error("migrate", "error", err)
		os.Exit(1)
	}

	server := api.New(persistence.NewPostgresRepository(pool), logger, api.CORSConfig{Origins: cfg.corsOrigins})
	server.Addr = cfg.httpAddr

	go func() {
		logger.Info("http listen", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http serve", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown", "error", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
