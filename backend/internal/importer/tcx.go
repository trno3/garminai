package importer

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/garmin/workout-viewer/backend/internal/domain"
)

// TCXAdapter parses Garmin TrainingCenterXML (TCX) files.
//
// Limitations: only the common fields are read. Power, cadence and moving-time
// metrics are best-effort; anything absent is left nil so it is never shown as
// zero. Lap-level Metrics blocks contribute maximum/minimum aggregates.
type TCXAdapter struct{}

var _ Adapter = (*TCXAdapter)(nil)

func (*TCXAdapter) Format() string { return "tcx" }

type tcxFile struct {
	Activities struct {
		Activities []tcxActivity `xml:"Activity"`
	} `xml:"Activities"`
}

type tcxActivity struct {
	Sport   string      `xml:"Sport,attr"`
	Id      string      `xml:"Id,attr"`
	Lap     []tcxLap    `xml:"Lap"`
	Metrics *tcxMetrics `xml:"Metrics"`
	Points  []tcxPoint  `xml:"Track>TrackPoint"`
}

type tcxLap struct {
	StartTime     string `xml:"StartTime,attr"`
	Duration      string `xml:"Duration"`
	Distance      string `xml:"DistanceMeters"`
	Calories      string `xml:"Calories"`
	AvgHeartRate  string `xml:"AverageHeartRateMec"`
	MaxHeartRate  string `xml:"MaxHeartRateMec"`
	AvgMovingTime string `xml:"AverageRunningCadence"` // informational
}

type tcxMetrics struct {
	Type  string `xml:"Type"`
	Value string `xml:"Value"`
}

type tcxPoint struct {
	Distance  string       `xml:"Distance"`
	Time      string       `xml:"Time"`
	Speed     string       `xml:"Speed"`
	Position  *tcxPosition `xml:"Position"`
	HeartRate *struct {
		Value string `xml:"Value"`
	} `xml:"HeartRateBPM"`
	Cadence string `xml:"Cadence"`
	Power   string `xml:"Power"`
}

type tcxPosition struct {
	Latitude  string `xml:"LatitudeDegrees"`
	Longitude string `xml:"LongitudeDegrees"`
	Elevation string `xml:"ElevationMeters"`
}

// Parse decodes a TCX document. It supports exactly one activity per file, which
// matches how Garmin exports represent a single workout.
func (*TCXAdapter) Parse(data []byte) (*Parsed, error) {
	var f tcxFile
	if err := xml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("invalid XML: %w", err)
	}
	acts := f.Activities.Activities
	if len(acts) == 0 {
		return nil, fmt.Errorf("no <Activity> elements found")
	}
	if len(acts) > 1 {
		return nil, fmt.Errorf("multiple activities not supported (%d found)", len(acts))
	}
	a := acts[0]

	parsed := &Parsed{Activity: &domain.Activity{
		SourceID: a.Id,
		Name:     strings.TrimSpace(a.Id),
		Sport:    normalizeTCXSport(a.Sport),
	}}
	if parsed.Activity.Name == "" {
		parsed.Activity.Name = string(parsed.Activity.Sport)
	}

	var (
		startTime time.Time
		lastDist  float64
		hasDist   bool
	)

	// Laps.
	for i, lap := range a.Lap {
		ls, ok := parseTime(lap.StartTime)
		if !ok {
			continue
		}
		if startTime.IsZero() {
			startTime = ls
		}
		dur := parseFloat64(lap.Duration)
		l := &domain.Lap{
			ID:              fmt.Sprintf("%s-lap-%d", parsed.Activity.ID, i+1),
			ActivityID:      parsed.Activity.ID,
			Index:           i + 1,
			StartTimestamp:  ls.UTC(),
			DurationSeconds: dur,
			DistanceMeters:  parseFloat64(lap.Distance),
			Calories:        parseFloat64(lap.Calories),
			AvgHeartRate:    parseInt32(lap.AvgHeartRate),
		}
		parsed.Laps = append(parsed.Laps, l)
	}

	// Track points -> samples and activity aggregates.
	for _, p := range a.Points {
		pt, ok := parseTime(p.Time)
		if !ok {
			continue
		}
		if startTime.IsZero() {
			startTime = pt
		}
		if d := parseFloat64(p.Distance); d != nil {
			lastDist = *d
			hasDist = true
		}
		addTCXPointSamples(parsed, pt.UTC(), p)
	}

	// Activity-level aggregates.
	parsed.Activity.StartTimestamp = startTime
	if len(a.Points) > 0 && !startTime.IsZero() {
		if et, ok := parseTime(a.Points[len(a.Points)-1].Time); ok {
			end := et.UTC()
			parsed.Activity.EndTimestamp = &end
			d := end.Sub(startTime).Seconds()
			if d > 0 {
				parsed.Activity.DurationSeconds = &d
			}
		}
	}
	if hasDist {
		parsed.Activity.DistanceMeters = &lastDist
	}
	avgHR, maxHR := lapHeartRates(a.Lap)
	parsed.Activity.AvgHeartRate = avgHR
	parsed.Activity.MaxHeartRate = maxHR

	if parsed.Activity.SourceID == "" {
		return nil, fmt.Errorf("activity has no <Id>")
	}
	return parsed, nil
}

func addTCXPointSamples(p *Parsed, t time.Time, pnt tcxPoint) {
	actID := p.Activity.ID
	if pnt.Position != nil {
		if lat := parseFloat64(pnt.Position.Latitude); lat != nil {
			if lon := parseFloat64(pnt.Position.Longitude); lon != nil {
				p.Samples = append(p.Samples, &domain.Sample{
					ID:         fmt.Sprintf("%s-%d-gps", actID, t.UnixNano()),
					ActivityID: actID,
					Kind:       domain.MetricGPS,
					Timestamp:  t,
					Latitude:   lat,
					Longitude:  lon,
				})
			}
		}
		if el := parseFloat64(pnt.Position.Elevation); el != nil {
			p.Samples = append(p.Samples, sample(actID, domain.MetricElevation, t, *el))
		}
	}
	if pnt.HeartRate != nil {
		if hr := parseFloat64(pnt.HeartRate.Value); hr != nil {
			p.Samples = append(p.Samples, sample(actID, domain.MetricHeartRate, t, *hr))
		}
	}
	if v := parseFloat64(pnt.Speed); v != nil {
		p.Samples = append(p.Samples, sample(actID, domain.MetricSpeed, t, *v))
	}
	if c := parseFloat64(pnt.Cadence); c != nil {
		p.Samples = append(p.Samples, sample(actID, domain.MetricCadence, t, *c))
	}
	if pw := parseFloat64(pnt.Power); pw != nil {
		p.Samples = append(p.Samples, sample(actID, domain.MetricPower, t, *pw))
	}
}

func lapHeartRates(laps []tcxLap) (avg *int, max *int) {
	var sum int
	var n int
	for _, l := range laps {
		if hr := parseInt32(l.AvgHeartRate); hr != nil {
			sum += *hr
			n++
		}
	}
	if n > 0 {
		a := sum / n
		avg = &a
	}
	for _, l := range laps {
		if hr := parseInt32(l.MaxHeartRate); hr != nil {
			if max == nil || *hr > *max {
				max = hr
			}
		}
	}
	return avg, max
}

func sample(activityID string, kind domain.MetricKind, t time.Time, v float64) *domain.Sample {
	return &domain.Sample{
		ID:         fmt.Sprintf("%s-%d-%s", activityID, t.UnixNano(), kind),
		ActivityID: activityID,
		Kind:       kind,
		Timestamp:  t,
		Value:      &v,
	}
}

// normalizeTCXSport maps a TCX Sport attribute to a lowercase slug.
func normalizeTCXSport(s string) domain.Sport {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "run":
		return domain.Sport("run")
	case "biking", "cycling":
		return domain.Sport("ride")
	case "walking":
		return domain.Sport("walk")
	case "crosscountrieskiing", "skying":
		return domain.Sport("skiing")
	case "swimming":
		return domain.Sport("swim")
	default:
		return domain.Sport("other")
	}
}
