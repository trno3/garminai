-- 001_init: core schema for the workout viewer.
-- Activities are stored separately from their time-series samples and laps so
-- large series stay cheap to query.

CREATE TABLE IF NOT EXISTS activities (
    id                TEXT PRIMARY KEY,
    source_id         TEXT NOT NULL UNIQUE,
    name              TEXT NOT NULL DEFAULT '',
    sport             TEXT NOT NULL DEFAULT 'other',
    start_timestamp   TIMESTAMPTZ NOT NULL,
    end_timestamp     TIMESTAMPTZ,
    timezone          TEXT,
    duration_seconds  DOUBLE PRECISION,
    moving_duration_s DOUBLE PRECISION,
    distance_meters   DOUBLE PRECISION,
    calories          DOUBLE PRECISION,
    avg_heart_rate    INTEGER,
    max_heart_rate    INTEGER,
    avg_power         INTEGER,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_activities_start ON activities (start_timestamp);
CREATE INDEX IF NOT EXISTS idx_activities_sport ON activities (sport);

CREATE TABLE IF NOT EXISTS laps (
    id                TEXT PRIMARY KEY,
    activity_id       TEXT NOT NULL REFERENCES activities (id) ON DELETE CASCADE,
    index             INTEGER NOT NULL,
    start_timestamp   TIMESTAMPTZ NOT NULL,
    end_timestamp     TIMESTAMPTZ,
    duration_seconds  DOUBLE PRECISION,
    moving_duration_s DOUBLE PRECISION,
    distance_meters   DOUBLE PRECISION,
    calories          DOUBLE PRECISION,
    avg_heart_rate    INTEGER,
    avg_power         INTEGER
);

CREATE INDEX IF NOT EXISTS idx_laps_activity ON laps (activity_id);

CREATE TABLE IF NOT EXISTS activity_samples (
    id            TEXT PRIMARY KEY,
    activity_id   TEXT NOT NULL REFERENCES activities (id) ON DELETE CASCADE,
    kind          TEXT NOT NULL,
    timestamp     TIMESTAMPTZ NOT NULL,
    value         DOUBLE PRECISION,
    latitude      DOUBLE PRECISION,
    longitude     DOUBLE PRECISION
);

CREATE INDEX IF NOT EXISTS idx_samples_activity ON activity_samples (activity_id, kind, timestamp);
