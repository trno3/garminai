// Package domain holds the platform-independent model of the workout viewer.
//
// Values here never carry HTTP, persistence or wire concerns so they can be
// shared by the importer, persistence and API layers without coupling.
package domain

import "time"

// Sport is a normalized sport/activity type. Values are lower-case slugs such
// as "run", "ride", "walk" or "other". Normalization happens in the importer so
// downstream code and the UI can rely on a stable vocabulary.
type Sport string

// MetricKind identifies one time-series series stored per activity.
type MetricKind string

const (
	MetricHeartRate   MetricKind = "heart_rate"
	MetricSpeed       MetricKind = "speed"
	MetricMovingSpeed MetricKind = "moving_speed"
	MetricElevation   MetricKind = "elevation"
	MetricPower       MetricKind = "power"
	MetricCadence     MetricKind = "cadence"
	MetricDistance    MetricKind = "distance"
	MetricTemperature MetricKind = "temperature"
	// MetricGPS holds coordinate samples used to draw the route map. It is not
	// surfaced as a chartable metric.
	MetricGPS MetricKind = "gps"
)

// Activity is a single imported workout.
//
// All timestamps are UTC. Optional fields stay nil when the source data does
// not provide them so missing metrics are never represented as zero.
type Activity struct {
	ID                string     `json:"id"`
	SourceID          string     `json:"source_id"`
	Name              string     `json:"name"`
	Sport             Sport      `json:"sport"`
	StartTimestamp    time.Time  `json:"start_timestamp"`
	EndTimestamp      *time.Time `json:"end_timestamp,omitempty"`
	Timezone          string     `json:"timezone,omitempty"`
	DurationSeconds   *float64   `json:"duration_seconds,omitempty"`
	MovingDurationSec *float64   `json:"moving_duration_seconds,omitempty"`
	DistanceMeters    *float64   `json:"distance_meters,omitempty"`
	Calories          *float64   `json:"calories,omitempty"`
	AvgHeartRate      *int       `json:"avg_heart_rate,omitempty"`
	MaxHeartRate      *int       `json:"max_heart_rate,omitempty"`
	AvgPower          *int       `json:"avg_power,omitempty"`
}

// Lap is a split of an activity into contiguous segments.
type Lap struct {
	ID                string     `json:"id"`
	ActivityID        string     `json:"activity_id"`
	Index             int        `json:"index"`
	StartTimestamp    time.Time  `json:"start_timestamp"`
	EndTimestamp      *time.Time `json:"end_timestamp,omitempty"`
	DurationSeconds   *float64   `json:"duration_seconds,omitempty"`
	MovingDurationSec *float64   `json:"moving_duration_seconds,omitempty"`
	DistanceMeters    *float64   `json:"distance_meters,omitempty"`
	Calories          *float64   `json:"calories,omitempty"`
	AvgHeartRate      *int       `json:"avg_heart_rate,omitempty"`
	AvgPower          *int       `json:"avg_power,omitempty"`
}

// Sample is one time-series point. Samples are stored separately from the
// activity row so large series stay cheap to query and filter.
type Sample struct {
	ID         string     `json:"id"`
	ActivityID string     `json:"activity_id"`
	Kind       MetricKind `json:"kind"`
	Timestamp  time.Time  `json:"timestamp"`
	Value      *float64   `json:"value,omitempty"`
	Latitude   *float64   `json:"latitude,omitempty"`
	Longitude  *float64   `json:"longitude,omitempty"`
}

// MetricList returns the distinct metric kinds available for an activity.
type MetricList struct {
	Metrics []MetricKind `json:"metrics"`
}

// Pagination carries request-side cursor state for list endpoints.
type Pagination struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// ActivityPage is a paginated slice of activities plus total count.
type ActivityPage struct {
	Total int64       `json:"total"`
	Items []*Activity `json:"items"`
}

// Summary aggregates dashboard metrics over an optional time window.
type Summary struct {
	ActivityCount   int64    `json:"activity_count"`
	DistanceMeters  *float64 `json:"distance_meters,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	Calories        *float64 `json:"calories,omitempty"`
	AvgHeartRate    *int     `json:"avg_heart_rate,omitempty"`
}

// SportDistribution maps a normalized sport to its activity count.
type SportDistribution struct {
	Sport Sport `json:"sport"`
	Count int64 `json:"count"`
}
