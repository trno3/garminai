package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// parseTime parses an RFC3339 timestamp, tolerating trailing 'Z' and offsets.
// Returns ok=false when the value is empty or unparseable so callers can leave
// the field nil rather than inventing a value.
func parseTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		// Some TCX/GPX files use space-separated or truncated forms.
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC(), true
			}
		}
		return time.Time{}, false
	}
	return t.UTC(), true
}

// parseFloat64 converts a string to *float64, returning nil for empty/invalid.
func parseFloat64(v string) *float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

// parseInt32 converts a string to *int, returning nil for empty/invalid.
func parseInt32(v string) *int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

// activityID derives a stable primary-key id from the source id so re-imports of
// identical data map to the same row.
func activityID(sourceID string) string {
	sum := sha256.Sum256([]byte(sourceID))
	return hex.EncodeToString(sum[:])
}
