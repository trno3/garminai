package importer

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/garmin/workout-viewer/backend/internal/domain"
	"github.com/garmin/workout-viewer/backend/internal/persistence"
)

// ---------------------------------------------------------------------------
// FIT binary builder
// ---------------------------------------------------------------------------

type fitDefn struct {
	dataType uint8
}

func fitU8(v uint8) []byte { return []byte{v} }
func fitU16(v uint16) []byte {
	return []byte{byte(v), byte(v >> 8)}
}
func fitU32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}
func fitS32(v int32) []byte { return fitU32(uint32(v)) }
func fitF32(v float32) []byte {
	return fitU32(math.Float32bits(v))
}

// fitMessage builds a standard (non-local) FIT message.
func fitMessage(globalNum uint16, defs []fitDefn, values [][]byte) []byte {
	numFields := len(defs)
	defSize := 1 + 3*numFields + 2 // numFields byte + 3 bytes/def + global num (u16)
	var msg []byte
	if defSize > 15 {
		msg = append(msg, 0x80, uint8(defSize)) // standard, extended length (low nibble 0)
	} else {
		msg = append(msg, 0x80|uint8(defSize)) // standard, length in low nibble
	}
	msg = append(msg, uint8(numFields))
	for _, d := range defs {
		msg = append(msg, d.dataType, 0, 0) // dataType, array=0, development=0
	}
	msg = append(msg, byte(globalNum&0xFF), byte((globalNum>>8)&0xFF))
	for _, v := range values {
		msg = append(msg, v...)
	}
	return msg
}

// fitFileIDType constants (base types used in the fixture).
const (
	typeUint8    = 0x00
	typeUint16   = 0x01
	typeUint32   = 0x02 // uint32_z decodes identically for non-zero values
	typeS32      = 0x6D
	typeFloat32  = 0x0A
	utcTimestamp = 0x02 // fit_utc_timestamp is a uint32_z
)

// buildFitFile assembles a minimal but structurally valid FIT file: header,
// one file_id message, one session message and one record message.
func buildFitFile() []byte {
	var body []byte

	// file_id (msg 20): manufacturer, product, serial_number, time_created.
	body = append(body, fitMessage(20, []fitDefn{
		{typeUint8}, {typeUint16}, {typeUint32}, {utcTimestamp},
	}, [][]byte{
		fitU8(1),    // manufacturer
		fitU16(42),  // product
		fitU32(999), // serial number
		fitU32(0),   // time created
	})...)

	// session (msg 3): start, distance, calories, elapsed, avgHR, maxHR.
	body = append(body, fitMessage(3, []fitDefn{
		{utcTimestamp}, {typeUint32}, {typeUint16}, {typeUint32}, {typeUint8}, {typeUint8},
	}, [][]byte{
		fitU32(100), // start time (seconds since FIT epoch)
		fitU32(1000),
		fitU16(500),
		fitU32(3600),
		fitU8(150),
		fitU8(180),
	})...)

	// record (msg 10): timestamp, lat, lon, altitude, speed, HR, cadence, power.
	lat := int32(math.Round(40.0 / 90.0 * 2147483648.0))
	lon := int32(math.Round(-73.0 / 90.0 * 2147483648.0))
	body = append(body, fitMessage(10, []fitDefn{
		{typeUint32}, {typeS32}, {typeS32}, {typeFloat32}, {typeUint16}, {typeUint8}, {typeUint8}, {typeUint16},
	}, [][]byte{
		fitU32(5), // timestamp (seconds since session start)
		fitS32(lat),
		fitS32(lon),
		fitF32(10.5),
		fitU16(0), // speed (base type only; not emitted as a sample)
		fitU8(155),
		fitU8(80),
		fitU16(200),
	})...)

	size := len(body)
	header := []byte{0x23, 0x08}
	header = append(header, fitU16(uint16(size))...)
	header = append(header, ".FIT"...)    // 4 bytes ".FIT"
	header = append(header, fitU16(0)...) // protocol version
	header = append(header, fitU16(0)...) // profile version

	out := make([]byte, 0, len(header)+len(body))
	out = append(out, header...)
	out = append(out, body...)
	return out
}

// ---------------------------------------------------------------------------
// In-memory fake repository (captures upserted data for assertions)
// ---------------------------------------------------------------------------

type fakeRepo struct {
	mu           sync.Mutex
	seen         map[string]bool
	duplicates   int
	lastActivity *domain.Activity
	lastLaps     []*domain.Lap
	lastSamples  []*domain.Sample
}

func (f *fakeRepo) UpsertActivity(_ context.Context, a *domain.Activity, laps []*domain.Lap, samples []*domain.Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[a.SourceID] {
		f.duplicates++
		return persistence.ErrDuplicateSource
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	f.seen[a.SourceID] = true
	f.lastActivity = a
	f.lastLaps = laps
	f.lastSamples = samples
	return nil
}

func (f *fakeRepo) GetActivity(_ context.Context, id string) (*domain.Activity, error) {
	return nil, persistence.ErrNotFound
}
func (f *fakeRepo) GetLaps(_ context.Context, _ string) ([]*domain.Lap, error) { return nil, nil }
func (f *fakeRepo) GetSamples(_ context.Context, _ string, _ *domain.MetricKind) ([]*domain.Sample, error) {
	return nil, nil
}
func (f *fakeRepo) GetMetrics(_ context.Context, _ string) (*domain.MetricList, error) {
	return &domain.MetricList{}, nil
}
func (f *fakeRepo) ListActivities(_ context.Context, _ persistence.Filter, _ domain.Pagination) (*domain.ActivityPage, error) {
	return &domain.ActivityPage{}, nil
}
func (f *fakeRepo) Summary(_ context.Context, _ *persistence.Window) (*domain.Summary, error) {
	return &domain.Summary{}, nil
}
func (f *fakeRepo) SportDistribution(_ context.Context) ([]*domain.SportDistribution, error) {
	return nil, nil
}

func (f *fakeRepo) captured() (*domain.Activity, []*domain.Lap, []*domain.Sample) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastActivity, f.lastLaps, f.lastSamples
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name string
		file string
		data []byte
		want string
	}{
		{"fit magic", "a.fit", buildFitFile(), "fit"},
		{"tcx xml", "a.tcx", []byte("<TrainingCenterDatabase></TrainingCenterDatabase>"), "tcx"},
		{"gpx xml", "a.gpx", []byte("<gpx></gpx>"), "gpx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DetectFormat(c.file, c.data)
			if err != nil {
				t.Fatalf("DetectFormat: %v", err)
			}
			if got != c.want {
				t.Fatalf("DetectFormat(%q) = %q, want %q", c.file, got, c.want)
			}
		})
	}
}

func TestParseFit(t *testing.T) {
	parsed, err := (&FITAdapter{}).Parse(buildFitFile())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := parsed.Activity
	if a.SourceID == "" {
		t.Fatal("expected source id from serial number")
	}
	wantStart := fitUtcTimestamp(100)
	if !a.StartTimestamp.Equal(wantStart) {
		t.Errorf("start = %v, want %v", a.StartTimestamp, wantStart)
	}
	if a.DurationSeconds == nil || *a.DurationSeconds != 3600 {
		t.Errorf("duration = %v, want 3600", a.DurationSeconds)
	}
	if a.DistanceMeters == nil || *a.DistanceMeters != 1000 {
		t.Errorf("distance = %v, want 1000", a.DistanceMeters)
	}
	if a.Calories == nil || *a.Calories != 500 {
		t.Errorf("calories = %v, want 500", a.Calories)
	}
	if a.AvgHeartRate == nil || *a.AvgHeartRate != 150 {
		t.Errorf("avgHR = %v, want 150", a.AvgHeartRate)
	}
	if a.MaxHeartRate == nil || *a.MaxHeartRate != 180 {
		t.Errorf("maxHR = %v, want 180", a.MaxHeartRate)
	}

	var hr, cad, pw, elev, gps int
	for _, s := range parsed.Samples {
		switch s.Kind {
		case domain.MetricHeartRate:
			hr++
		case domain.MetricCadence:
			cad++
		case domain.MetricPower:
			pw++
		case domain.MetricElevation:
			elev++
		case domain.MetricGPS:
			gps++
		}
	}
	if hr != 1 || cad != 1 || pw != 1 || elev != 1 || gps != 1 {
		t.Errorf("sample kinds = hr:%d cad:%d pw:%d elev:%d gps:%d, want all 1", hr, cad, pw, elev, gps)
	}

	for _, s := range parsed.Samples {
		if s.Kind == domain.MetricGPS {
			if s.Latitude == nil || s.Longitude == nil {
				t.Fatal("gps sample missing coordinates")
			}
			if math.Abs(*s.Latitude-40.0) > 0.01 {
				t.Errorf("latitude = %v, want ~40", *s.Latitude)
			}
			if math.Abs(*s.Longitude+73.0) > 0.01 {
				t.Errorf("longitude = %v, want ~-73", *s.Longitude)
			}
		}
	}
}

func TestParseFitNoSession(t *testing.T) {
	body := fitMessage(20, []fitDefn{{typeUint8}, {typeUint16}, {typeUint32}, {utcTimestamp}},
		[][]byte{fitU8(1), fitU16(42), fitU32(999), fitU32(0)})
	size := len(body)
	header := []byte{0x23, 0x08}
	header = append(header, fitU16(uint16(size))...)
	header = append(header, ".FIT"...)
	header = append(header, fitU16(0)...)
	header = append(header, fitU16(0)...)
	data := append(header, body...)

	if _, err := (&FITAdapter{}).Parse(data); err == nil {
		t.Fatal("expected error for FIT with no session/lap/record messages")
	}
}

func TestServiceImportFit(t *testing.T) {
	svc := NewService(&fakeRepo{}, nil, 0)
	res, err := svc.Import(buildFitFile(), "run.fit")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Format != "fit" {
		t.Errorf("format = %q, want fit", res.Format)
	}
	if res.SampleCount != 5 {
		t.Errorf("sample count = %d, want 5", res.SampleCount)
	}
	if res.ActivityID == "" {
		t.Error("expected activity id")
	}

	f := svc.repo.(*fakeRepo)
	if f.lastActivity == nil {
		t.Fatal("activity was not persisted")
	}

	// Second import of the same file is a duplicate.
	res2, err := svc.Import(buildFitFile(), "run.fit")
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if !res2.Duplicate {
		t.Error("expected Duplicate=true on second import")
	}
}

func TestServiceImportTCX(t *testing.T) {
	const tcx = `<?xml version="1.0" encoding="UTF-8"?>
<TrainingCenterDatabase>
  <Activities>
  <Activity Id="2024-05-01T08:00:00Z">
    <Name>Run</Name>
    <Sport>Run</Sport>
    <Lap StartTime="2024-05-01T08:00:00Z">
      <TotalTime>1800</TotalTime>
      <Distance>5000</Distance>
      <Calories>400</Calories>
      <Intensity>Active</Intensity>
      <TriggerEvent>Begin</TriggerEvent>
      <Metrics>
        <AverageHeartRate>140</AverageHeartRate>
        <MaxHeartRate>170</MaxHeartRate>
      </Metrics>
    </Lap>
    <Track>
      <TrackPoint>
        <Time>2024-05-01T08:00:10Z</Time>
        <HeartRateBPM><Value>145</Value></HeartRateBPM>
        <Distance>250</Distance>
        <Cadence>85</Cadence>
        <Position><LatitudeDegrees>40.0</LatitudeDegrees><LongitudeDegrees>-73.0</LongitudeDegrees></Position>
      </TrackPoint>
      <TrackPoint>
        <Time>2024-05-01T08:00:20Z</Time>
        <HeartRateBPM><Value>150</Value></HeartRateBPM>
        <Cadence>87</Cadence>
        <Position><LatitudeDegrees>40.1</LatitudeDegrees><LongitudeDegrees>-73.1</LongitudeDegrees></Position>
      </TrackPoint>
    </Track>
  </Activity>
  </Activities>
</TrainingCenterDatabase>`

	svc := NewService(&fakeRepo{}, nil, 0)
	res, err := svc.Import([]byte(tcx), "run.tcx")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Format != "tcx" {
		t.Errorf("format = %q, want tcx", res.Format)
	}
	if res.LapCount != 1 {
		t.Errorf("lap count = %d, want 1", res.LapCount)
	}
	if res.SampleCount != 6 { // 2 GPS + 2 HR + 2 cadence per point
		t.Errorf("sample count = %d, want 6", res.SampleCount)
	}
}

func TestServiceImportGPX(t *testing.T) {
	const gpx = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="test" source="gpsies">
  <trk name="Evening Loop">
    <type>Run</type>
    <trkseg>
      <trkpt lat="40.0" lon="-73.0"><ele>10.0</ele><time>2024-05-01T18:00:00Z</time><hr>140</hr></trkpt>
      <trkpt lat="40.1" lon="-73.1"><ele>12.0</ele><time>2024-05-01T18:00:10Z</time><hr>150</hr></trkpt>
    </trkseg>
  </trk>
</gpx>`

	svc := NewService(&fakeRepo{}, nil, 0)
	res, err := svc.Import([]byte(gpx), "loop.gpx")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Format != "gpx" {
		t.Errorf("format = %q, want gpx", res.Format)
	}
	if res.SampleCount != 6 { // 2 ele + 2 HR + 2 GPS per point
		t.Errorf("sample count = %d, want 6", res.SampleCount)
	}
	f := svc.repo.(*fakeRepo)
	if f.lastActivity == nil || f.lastActivity.Name != "Evening Loop" {
		t.Errorf("expected persisted name Evening Loop, got %+v", f.lastActivity)
	}
}

func TestServiceImportErrors(t *testing.T) {
	svc := NewService(&fakeRepo{}, nil, 0)

	t.Run("empty", func(t *testing.T) {
		if _, err := svc.Import(nil, "empty.bin"); err == nil {
			t.Fatal("expected error for empty file")
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		if _, err := svc.Import([]byte("not a real file"), "weird.bin"); err == nil {
			t.Fatal("expected error for unsupported format")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		big := make([]byte, MaxUploadSizeBytes+1)
		if _, err := svc.Import(big, "big.tcx"); err == nil {
			t.Fatal("expected error for oversized file")
		}
	})

	t.Run("malformed xml", func(t *testing.T) {
		if _, err := svc.Import([]byte("<gpx><trk>unclosed"), "bad.gpx"); err == nil {
			t.Fatal("expected error for malformed XML")
		}
	})
}

func TestDuplicateSentinel(t *testing.T) {
	f := &fakeRepo{}
	_ = f.UpsertActivity(context.Background(), &domain.Activity{SourceID: "x"}, nil, nil)
	err := f.UpsertActivity(context.Background(), &domain.Activity{SourceID: "x"}, nil, nil)
	if !errors.Is(err, persistence.ErrDuplicateSource) {
		t.Fatalf("sentinel not propagated: %v", err)
	}
}
