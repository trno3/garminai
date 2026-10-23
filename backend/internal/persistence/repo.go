package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/garmin/workout-viewer/backend/internal/domain"
)

// Sentinel errors shared across the persistence layer.
var (
	// ErrNotFound is returned when a requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrDuplicateSource is returned when inserting an activity whose source ID
	// already exists. Importers use this to stay idempotent.
	ErrDuplicateSource = errors.New("duplicate source id")
)

// ActivityRepository persists activities, laps and time-series samples and
// answers the read queries used by the API. All methods wrap returned errors so
// callers can match sentinel values with errors.Is.
type ActivityRepository interface {
	// UpsertActivity inserts an activity and its laps and samples inside a
	// single transaction. A duplicate source id returns ErrDuplicateSource
	// instead of failing, letting importers report idempotent skips.
	UpsertActivity(ctx context.Context, a *domain.Activity, laps []*domain.Lap, samples []*domain.Sample) error

	GetActivity(ctx context.Context, id string) (*domain.Activity, error)
	GetLaps(ctx context.Context, activityID string) ([]*domain.Lap, error)
	// GetSamples returns one metric series. When kind is nil all series for the
	// activity are returned, grouped by kind on the caller side.
	GetSamples(ctx context.Context, activityID string, kind *domain.MetricKind) ([]*domain.Sample, error)
	GetMetrics(ctx context.Context, activityID string) (*domain.MetricList, error)

	ListActivities(ctx context.Context, f Filter, p domain.Pagination) (*domain.ActivityPage, error)
	Summary(ctx context.Context, w *Window) (*domain.Summary, error)
	SportDistribution(ctx context.Context) ([]*domain.SportDistribution, error)
}

// Filter narrows a list query. Empty fields are ignored.
type Filter struct {
	Sport *domain.Sport
	Start *time.Time
	End   *time.Time
	Text  string // matched against activity name with ILIKE
}

// Window bounds a summary query to [Start, End). Both may be nil for all-time.
type Window struct {
	Start *time.Time
	End   *time.Time
}
