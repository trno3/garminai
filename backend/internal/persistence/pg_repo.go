package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/garmin/workout-viewer/backend/internal/domain"
)

// PgRepo is the pgx-backed ActivityRepository.
type PgRepo struct {
	pool *pgxpool.Pool
}

var _ ActivityRepository = (*PgRepo)(nil)

// NewPostgresRepository builds a repository over an existing connection pool.
func NewPostgresRepository(pool *pgxpool.Pool) *PgRepo {
	return &PgRepo{pool: pool}
}

// Ping verifies the database connection. It is used by the readiness probe.
func (r *PgRepo) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

const insertActivity = `
INSERT INTO activities
    (id, source_id, name, sport, start_timestamp, end_timestamp, timezone,
     duration_seconds, moving_duration_s, distance_meters, calories,
     avg_heart_rate, max_heart_rate, avg_power)
VALUES
    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (source_id) DO NOTHING`

const insertLap = `
INSERT INTO laps
    (id, activity_id, index, start_timestamp, end_timestamp, duration_seconds,
     moving_duration_s, distance_meters, calories, avg_heart_rate, avg_power)
VALUES
    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING`

const insertSample = `
INSERT INTO activity_samples
    (id, activity_id, kind, timestamp, value, latitude, longitude)
VALUES
    ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO NOTHING`

// UpsertActivity inserts an activity with its laps and samples atomically.
func (r *PgRepo) UpsertActivity(ctx context.Context, a *domain.Activity, laps []*domain.Lap, samples []*domain.Sample) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	res, err := tx.Exec(ctx, insertActivity,
		a.ID, a.SourceID, a.Name, string(a.Sport), a.StartTimestamp, a.EndTimestamp,
		a.Timezone, a.DurationSeconds, a.MovingDurationSec, a.DistanceMeters,
		a.Calories, a.AvgHeartRate, a.MaxHeartRate, a.AvgPower,
	)
	if err != nil {
		return fmt.Errorf("insert activity: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrDuplicateSource
	}

	for _, l := range laps {
		if _, err := tx.Exec(ctx, insertLap,
			l.ID, l.ActivityID, l.Index, l.StartTimestamp, l.EndTimestamp,
			l.DurationSeconds, l.MovingDurationSec, l.DistanceMeters, l.Calories,
			l.AvgHeartRate, l.AvgPower,
		); err != nil {
			return fmt.Errorf("insert lap: %w", err)
		}
	}
	for _, s := range samples {
		if _, err := tx.Exec(ctx, insertSample,
			s.ID, s.ActivityID, string(s.Kind), s.Timestamp, s.Value, s.Latitude, s.Longitude,
		); err != nil {
			return fmt.Errorf("insert sample: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (r *PgRepo) GetActivity(ctx context.Context, id string) (*domain.Activity, error) {
	row := r.pool.QueryRow(ctx, `
SELECT id, source_id, name, sport, start_timestamp, end_timestamp, timezone,
       duration_seconds, moving_duration_s, distance_meters, calories,
       avg_heart_rate, max_heart_rate, avg_power
FROM activities WHERE id = $1`, id)
	a, err := scanActivity(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return a, nil
}

func (r *PgRepo) GetLaps(ctx context.Context, activityID string) ([]*domain.Lap, error) {
	rows, err := r.pool.Query(ctx, `
SELECT id, activity_id, index, start_timestamp, end_timestamp, duration_seconds,
       moving_duration_s, distance_meters, calories, avg_heart_rate, avg_power
FROM laps WHERE activity_id = $1 ORDER BY index`, activityID)
	if err != nil {
		return nil, fmt.Errorf("query laps: %w", err)
	}
	defer rows.Close()

	var laps []*domain.Lap
	for rows.Next() {
		l, err := scanLap(rows)
		if err != nil {
			return nil, err
		}
		laps = append(laps, l)
	}
	return laps, rows.Err()
}

func (r *PgRepo) GetSamples(ctx context.Context, activityID string, kind *domain.MetricKind) ([]*domain.Sample, error) {
	var (
		q          = `SELECT id, activity_id, kind, timestamp, value, latitude, longitude FROM activity_samples WHERE activity_id = $1`
		args []any = []any{activityID}
	)
	if kind != nil {
		q += ` AND kind = $2`
		args = append(args, string(*kind))
	}
	q += ` ORDER BY timestamp`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query samples: %w", err)
	}
	defer rows.Close()

	var samples []*domain.Sample
	for rows.Next() {
		s, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		samples = append(samples, s)
	}
	return samples, rows.Err()
}

func (r *PgRepo) GetMetrics(ctx context.Context, activityID string) (*domain.MetricList, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT kind FROM activity_samples WHERE activity_id = $1`, activityID)
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	defer rows.Close()

	var kinds []domain.MetricKind
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		kinds = append(kinds, domain.MetricKind(k))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(kinds) == 0 {
		return &domain.MetricList{}, nil
	}
	return &domain.MetricList{Metrics: kinds}, nil
}

func (r *PgRepo) ListActivities(ctx context.Context, f Filter, p domain.Pagination) (*domain.ActivityPage, error) {
	where, args := buildWhere(f)
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM activities`+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count activities: %w", err)
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, source_id, name, sport, start_timestamp, end_timestamp, timezone,
		        duration_seconds, moving_duration_s, distance_meters, calories,
		        avg_heart_rate, max_heart_rate, avg_power
		 FROM activities`+where+` ORDER BY start_timestamp DESC LIMIT $`+fmt.Sprintf("%d", len(args)+1)+` OFFSET $`+fmt.Sprintf("%d", len(args)+2),
		append(args, p.Limit, p.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("query activities: %w", err)
	}
	defer rows.Close()

	var items []*domain.Activity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return &domain.ActivityPage{Total: total, Items: items}, rows.Err()
}

func (r *PgRepo) Summary(ctx context.Context, w *Window) (*domain.Summary, error) {
	where, args := buildWindow(w)
	row := r.pool.QueryRow(ctx, `
SELECT COUNT(*),
       SUM(distance_meters), SUM(duration_seconds), SUM(calories),
       AVG(avg_heart_rate)
FROM activities`+where, args...)

	var (
		count    int64
		dist     sql.NullFloat64
		dur      sql.NullFloat64
		calories sql.NullFloat64
		avgHR    sql.NullFloat64
	)
	if err := row.Scan(&count, &dist, &dur, &calories, &avgHR); err != nil {
		return nil, fmt.Errorf("summary: %w", err)
	}
	if count == 0 {
		return &domain.Summary{}, nil
	}
	s := &domain.Summary{ActivityCount: count}
	if dist.Valid {
		s.DistanceMeters = &dist.Float64
	}
	if dur.Valid {
		s.DurationSeconds = &dur.Float64
	}
	if calories.Valid {
		s.Calories = &calories.Float64
	}
	if avgHR.Valid {
		h := int(avgHR.Float64 + 0.5)
		s.AvgHeartRate = &h
	}
	return s, nil
}

func (r *PgRepo) SportDistribution(ctx context.Context) ([]*domain.SportDistribution, error) {
	rows, err := r.pool.Query(ctx, `SELECT sport, COUNT(*) FROM activities GROUP BY sport`)
	if err != nil {
		return nil, fmt.Errorf("sport distribution: %w", err)
	}
	defer rows.Close()

	var out []*domain.SportDistribution
	for rows.Next() {
		var s string
		var c int64
		if err := rows.Scan(&s, &c); err != nil {
			return nil, err
		}
		out = append(out, &domain.SportDistribution{Sport: domain.Sport(s), Count: c})
	}
	return out, rows.Err()
}

// scanActivity scans a single activity row into a pointer-bearing struct.
func scanActivity(row scanner) (*domain.Activity, error) {
	var a domain.Activity
	var (
		end    sql.NullTime
		dist   sql.NullFloat64
		dur    sql.NullFloat64
		movDur sql.NullFloat64
		cals   sql.NullFloat64
		avgHR  sql.NullInt64
		maxHR  sql.NullInt64
		avgPow sql.NullInt64
	)
	err := row.Scan(&a.ID, &a.SourceID, &a.Name, &a.Sport, &a.StartTimestamp, &end,
		&a.Timezone, &dur, &movDur, &dist, &cals, &avgHR, &maxHR, &avgPow)
	if err != nil {
		return nil, err
	}
	a.EndTimestamp = nullTimeToPtr(end)
	a.DurationSeconds = nullFloatToPtr(dur)
	a.MovingDurationSec = nullFloatToPtr(movDur)
	a.DistanceMeters = nullFloatToPtr(dist)
	a.Calories = nullFloatToPtr(cals)
	a.AvgHeartRate = nullIntToPtr(avgHR)
	a.MaxHeartRate = nullIntToPtr(maxHR)
	a.AvgPower = nullIntToPtr(avgPow)
	return &a, nil
}

func scanLap(rows rowsScanner) (*domain.Lap, error) {
	var l domain.Lap
	var (
		end    sql.NullTime
		dur    sql.NullFloat64
		movDur sql.NullFloat64
		dist   sql.NullFloat64
		cals   sql.NullFloat64
		avgHR  sql.NullInt64
		avgPow sql.NullInt64
	)
	err := rows.Scan(&l.ID, &l.ActivityID, &l.Index, &l.StartTimestamp, &end,
		&dur, &movDur, &dist, &cals, &avgHR, &avgPow)
	if err != nil {
		return nil, err
	}
	l.EndTimestamp = nullTimeToPtr(end)
	l.DurationSeconds = nullFloatToPtr(dur)
	l.MovingDurationSec = nullFloatToPtr(movDur)
	l.DistanceMeters = nullFloatToPtr(dist)
	l.Calories = nullFloatToPtr(cals)
	l.AvgHeartRate = nullIntToPtr(avgHR)
	l.AvgPower = nullIntToPtr(avgPow)
	return &l, nil
}

func scanSample(rows rowsScanner) (*domain.Sample, error) {
	var s domain.Sample
	var (
		val sql.NullFloat64
		lat sql.NullFloat64
		lon sql.NullFloat64
	)
	err := rows.Scan(&s.ID, &s.ActivityID, &s.Kind, &s.Timestamp, &val, &lat, &lon)
	if err != nil {
		return nil, err
	}
	s.Value = nullFloatToPtr(val)
	s.Latitude = nullFloatToPtr(lat)
	s.Longitude = nullFloatToPtr(lon)
	return &s, nil
}

// buildWhere assembles a WHERE clause and args from a Filter. The first arg is
// always the activity id placeholder for GetActivity; callers that need a bare
// clause can pass an empty Filter.
func buildWhere(f Filter) (string, []any) {
	var parts []string
	var args []any
	i := 1
	if f.Sport != nil {
		parts = append(parts, fmt.Sprintf("sport = $%d", i))
		args = append(args, string(*f.Sport))
		i++
	}
	if f.Start != nil {
		parts = append(parts, fmt.Sprintf("start_timestamp >= $%d", i))
		args = append(args, *f.Start)
		i++
	}
	if f.End != nil {
		parts = append(parts, fmt.Sprintf("start_timestamp < $%d", i))
		args = append(args, *f.End)
		i++
	}
	if strings.TrimSpace(f.Text) != "" {
		parts = append(parts, fmt.Sprintf("name ILIKE $%d", i))
		args = append(args, "%"+strings.ToLower(strings.TrimSpace(f.Text))+"%")
		i++
	}
	if len(parts) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}

func buildWindow(w *Window) (string, []any) {
	var parts []string
	var args []any
	i := 1
	if w != nil {
		if w.Start != nil {
			parts = append(parts, fmt.Sprintf("start_timestamp >= $%d", i))
			args = append(args, *w.Start)
			i++
		}
		if w.End != nil {
			parts = append(parts, fmt.Sprintf("start_timestamp < $%d", i))
			args = append(args, *w.End)
			i++
		}
	}
	if len(parts) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}

func nullTimeToPtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func nullFloatToPtr(f sql.NullFloat64) *float64 {
	if !f.Valid {
		return nil
	}
	v := f.Float64
	return &v
}

func nullIntToPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}
