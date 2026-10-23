// Package importer turns untrusted FIT/TCX/GPX files into normalized domain
// activities. Format parsing is delegated to per-format adapters; this package
// owns validation, format detection, deduplication and persistence wiring so the
// adapters stay focused on decoding.
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/garmin/workout-viewer/backend/internal/domain"
	"github.com/garmin/workout-viewer/backend/internal/persistence"
)

// MaxUploadSizeBytes caps accepted uploads. Files larger than this are rejected
// before any parsing to protect against resource exhaustion.
const MaxUploadSizeBytes int64 = 100 << 20 // 100 MiB

// Adapter decodes one file format into a normalized domain result.
type Adapter interface {
	// Format returns the canonical format name: "tcx", "gpx" or "fit".
	Format() string
	// Parse decodes raw file bytes. It must validate the content, not just the
	// extension, and return a descriptive error for malformed input.
	Parse(data []byte) (*Parsed, error)
}

// Parsed is the normalized output of an adapter.
type Parsed struct {
	Activity *domain.Activity
	Laps     []*domain.Lap
	Samples  []*domain.Sample
}

// Result reports the outcome of an Import call.
type Result struct {
	Format      string
	SourceID    string
	ActivityID  string
	LapCount    int
	SampleCount int
	// Duplicate is true when the activity already existed and nothing was
	// written. Importers use it to report idempotent skips.
	Duplicate bool
}

// Service wires adapters to persistence and enforces import policy.
type Service struct {
	adapters map[string]Adapter
	maxSize  int64
	repo     persistence.ActivityRepository
	log      *slog.Logger
}

// NewService builds an importer service. maxSize is the per-file byte limit.
func NewService(repo persistence.ActivityRepository, log *slog.Logger, maxSize int64) *Service {
	if maxSize <= 0 {
		maxSize = MaxUploadSizeBytes
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Service{repo: repo, log: log, maxSize: maxSize, adapters: map[string]Adapter{}}
	s.Register(&TCXAdapter{})
	s.Register(&GPXAdapter{})
	s.Register(&FITAdapter{})
	return s
}

// Register adds an adapter. Later registrations with the same Format replace
// earlier ones, which keeps tests able to substitute fakes.
func (s *Service) Register(adapters ...Adapter) {
	for _, a := range adapters {
		s.adapters[a.Format()] = a
	}
}

// Import validates, parses and persists a single file.
func (s *Service) Import(data []byte, filename string) (*Result, error) {
	if len(data) == 0 {
		return nil, errors.New("empty file")
	}
	if int64(len(data)) > s.maxSize {
		return nil, errors.New("file exceeds maximum upload size")
	}

	format, err := DetectFormat(filename, data)
	if err != nil {
		return nil, err
	}
	adapter, ok := s.adapters[format]
	if !ok {
		return nil, ErrUnsupportedFormat
	}

	parsed, err := adapter.Parse(data)
	if err != nil {
		return nil, wrapParseError(format, err)
	}
	if parsed.Activity == nil {
		return nil, errors.New("parser produced no activity")
	}
	if parsed.Activity.SourceID == "" {
		// Fall back to a content fingerprint so duplicates are still detected.
		sum := sha256.Sum256(data)
		parsed.Activity.SourceID = "file:" + hex.EncodeToString(sum[:])
	}
	// Derive a stable primary key from the source id so re-imports of identical
	// data map to the same row.
	if parsed.Activity.ID == "" {
		parsed.Activity.ID = activityID(parsed.Activity.SourceID)
	}

	err = s.repo.UpsertActivity(context.Background(), parsed.Activity, parsed.Laps, parsed.Samples)
	res := &Result{
		Format:      format,
		SourceID:    parsed.Activity.SourceID,
		ActivityID:  parsed.Activity.ID,
		LapCount:    len(parsed.Laps),
		SampleCount: len(parsed.Samples),
	}
	switch {
	case errors.Is(err, persistence.ErrDuplicateSource):
		res.Duplicate = true
		s.log.Info("duplicate import skipped", "format", format, "source_id", res.SourceID)
		return res, nil
	case err != nil:
		return nil, fmt.Errorf("persist %s import: %w", format, err)
	}
	s.log.Info("imported activity", "format", format, "source_id", res.SourceID, "laps", res.LapCount, "samples", res.SampleCount)
	return res, nil
}

// ErrUnsupportedFormat is returned when the content cannot be recognized.
var ErrUnsupportedFormat = errors.New("unsupported or unrecognized file format")

// DetectFormat identifies a format from actual file content, falling back to the
// file extension only when content sniffing is inconclusive.
func DetectFormat(filename string, data []byte) (string, error) {
	if isFIT(data) {
		return "fit", nil
	}
	if name, ok := peekXMLRoot(data); ok {
		switch name {
		case "TrainingCenterDatabase":
			return "tcx", nil
		case "gpx":
			return "gpx", nil
		}
	}
	// Fallback: trust the extension only when content gave no answer.
	switch strings.ToLower(path.Ext(filename)) {
	case ".tcx":
		return "tcx", nil
	case ".gpx":
		return "gpx", nil
	case ".fit":
		return "fit", nil
	}
	return "", ErrUnsupportedFormat
}

// isFIT reports whether data begins with a FIT header: 0x23 0x08, a little
// endian data-size, then the ASCII suffix ".FIT".
func isFIT(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	if data[0] != 0x23 || data[1] != 0x08 {
		return false
	}
	return bytes.HasPrefix(data[4:], []byte(".FIT"))
}

// peekXMLRoot returns the first element name in an XML document, if present.
func peekXMLRoot(data []byte) (string, bool) {
	i := bytes.IndexByte(data, '<')
	if i < 0 {
		return "", false
	}
	j := bytes.IndexByte(data[i:], '>')
	if j < 0 {
		return "", false
	}
	tag := data[i+1 : i+j]
	// Strip namespace prefix and attributes.
	if k := bytes.IndexAny(tag, ": \t\r\n"); k >= 0 {
		tag = tag[:k]
	}
	if len(tag) == 0 || tag[0] == '/' {
		return "", false
	}
	return string(tag), true
}

func wrapParseError(format string, err error) error {
	return fmt.Errorf("failed to parse %s file: %w", format, err)
}
