package persistence

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/garmin/workout-viewer/backend/internal/domain"
)

// newTestPool connects to the database named by PGTEST_DSN (falling back to a
// local default) and applies migrations. Tests skip when no DSN is available so
// `go test ./...` remains hermetic without a database.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PGTEST_DSN")
	if dsn == "" {
		t.Skip("PGTEST_DSN not set; skipping integration test")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, "TRUNCATE activity_samples, laps, activities"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func sampleActivity() *domain.Activity {
	start := time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC)
	dist := 10000.0
	dur := 3600.0
	hr := 150
	return &domain.Activity{
		ID:              "act-1",
		SourceID:        "src-1",
		Name:            "Morning Run",
		Sport:           domain.Sport("run"),
		StartTimestamp:  start,
		DurationSeconds: &dur,
		DistanceMeters:  &dist,
		AvgHeartRate:    &hr,
	}
}

func TestUpsertActivityIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewPostgresRepository(pool)

	act := sampleActivity()
	laps := []*domain.Lap{{ID: "lap-1", ActivityID: act.ID, Index: 1}}
	samples := []*domain.Sample{{ID: "s-1", ActivityID: act.ID, Kind: domain.MetricHeartRate, Timestamp: act.StartTimestamp, Value: ptrFloat(140)}}

	if err := repo.UpsertActivity(ctx, act, laps, samples); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	err := repo.UpsertActivity(ctx, act, laps, samples)
	if !errors.Is(err, ErrDuplicateSource) {
		t.Fatalf("second upsert err = %v, want ErrDuplicateSource", err)
	}

	got, err := repo.GetActivity(ctx, act.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DistanceMeters == nil || *got.DistanceMeters != 10000 {
		t.Fatalf("distance not persisted: %+v", got)
	}
	if got.AvgHeartRate == nil || *got.AvgHeartRate != 150 {
		t.Fatalf("hr not persisted: %+v", got)
	}

	lapsOut, err := repo.GetLaps(ctx, act.ID)
	if err != nil || len(lapsOut) != 1 {
		t.Fatalf("laps = %v, err %v", lapsOut, err)
	}
	samplesOut, err := repo.GetSamples(ctx, act.ID, nil)
	if err != nil || len(samplesOut) != 1 {
		t.Fatalf("samples = %v, err %v", samplesOut, err)
	}
}

func TestGetActivityNotFound(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewPostgresRepository(pool)

	_, err := repo.GetActivity(ctx, "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListActivitiesFilter(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewPostgresRepository(pool)

	mustUpsert(t, ctx, repo, sampleActivity())

	sport := domain.Sport("run")
	f := Filter{Sport: &sport}
	page, err := repo.ListActivities(ctx, f, domain.Pagination{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
}

func TestSummaryNeverZeroWhenEmpty(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewPostgresRepository(pool)

	sum, err := repo.Summary(ctx, nil)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.ActivityCount != 0 {
		t.Fatalf("count = %d, want 0", sum.ActivityCount)
	}
	if sum.DistanceMeters != nil || sum.DurationSeconds != nil {
		t.Fatalf("missing metrics must be nil, got %+v", sum)
	}
}

func TestSummaryWith(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewPostgresRepository(pool)

	mustUpsert(t, ctx, repo, sampleActivity())
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	wnd := &Window{Start: &start, End: &end}

	sum, err := repo.Summary(ctx, wnd)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.ActivityCount != 1 || sum.DistanceMeters == nil {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestSportDistribution(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewPostgresRepository(pool)

	mustUpsert(t, ctx, repo, sampleActivity())
	dist, err := repo.SportDistribution(ctx)
	if err != nil {
		t.Fatalf("distribution: %v", err)
	}
	if len(dist) != 1 || dist[0].Sport != domain.Sport("run") || dist[0].Count != 1 {
		t.Fatalf("distribution = %+v", dist)
	}
}

func TestPing(t *testing.T) {
	pool := newTestPool(t)
	if err := NewPostgresRepository(pool).Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func mustUpsert(t *testing.T, ctx context.Context, repo ActivityRepository, a *domain.Activity) {
	t.Helper()
	if err := repo.UpsertActivity(ctx, a, nil, nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func ptrFloat(f float64) *float64 { return &f }
