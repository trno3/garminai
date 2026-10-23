package importer

import (
	"encoding/xml"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/garmin/workout-viewer/backend/internal/domain"
)

// GPXAdapter parses GPS eXchange Format (GPX) tracks into a single activity.
//
// Limitations: GPX has no concept of calories or lap boundaries, and sport is
// usually unspecified, so those stay nil / "other". Distance is derived from the
// great-circle distance between consecutive track points when elevation/length
// metadata is absent.
type GPXAdapter struct{}

var _ Adapter = (*GPXAdapter)(nil)

func (*GPXAdapter) Format() string { return "gpx" }

type gpxFile struct {
	Source string     `xml:"source,attr"`
	Tracks []gpxTrack `xml:"trk"`
}

type gpxTrack struct {
	Name   string   `xml:"name,attr"`
	Source string   `xml:"source,attr"`
	Type   string   `xml:"type"`
	Segs   []gpxSeg `xml:"trkseg"`
}

type gpxSeg struct {
	Points []gpxPoint `xml:"trkpt"`
}

type gpxPoint struct {
	Time    string `xml:"time"`
	Ele     string `xml:"ele"`
	Lat     string `xml:"lat,attr"`
	Lon     string `xml:"lon,attr"`
	HR      string `xml:"hr"`
	Speed   string `xml:"speed"`
	Cadence string `xml:"cadence"`
}

// Parse decodes a GPX document. All tracks in the file are merged into one
// activity, ordered as they appear.
func (*GPXAdapter) Parse(data []byte) (*Parsed, error) {
	var f gpxFile
	if err := xml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("invalid XML: %w", err)
	}
	if len(f.Tracks) == 0 {
		return nil, fmt.Errorf("no <track> elements found")
	}

	// Flatten points in document order.
	var pts []gpxPoint
	for _, tr := range f.Tracks {
		for _, seg := range tr.Segs {
			pts = append(pts, seg.Points...)
		}
	}
	if len(pts) == 0 {
		return nil, fmt.Errorf("track contains no track points")
	}

	allNames := append([]string{f.Tracks[0].Name}, trackNames(f.Tracks)...)
	name := strings.TrimSpace(firstNonEmpty(allNames...))
	source := strings.TrimSpace(firstNonEmpty(f.Tracks[0].Source, f.Tracks[0].Type, f.Source))

	parsed := &Parsed{Activity: &domain.Activity{
		Name:     name,
		Sport:    normalizeGPXSport(f.Tracks[0].Type),
		SourceID: source,
	}}
	if parsed.Activity.Name == "" {
		parsed.Activity.Name = "GPX track"
	}

	var (
		startTime time.Time
		endTime   time.Time
		hasStart  bool
		prev      *gpxPoint
	)
	for _, p := range pts {
		t, ok := parseTime(p.Time)
		if !ok {
			continue
		}
		if !hasStart {
			startTime = t
			hasStart = true
		}
		endTime = t
		if p.Lat != "" && p.Lon != "" {
			addGPXPointSamples(parsed, t, p)
			if prev != nil && prev.Lat != "" && prev.Lon != "" {
				if d := haversine(prev, &p); d > 0 {
					parsed.Activity.DistanceMeters = addFloat(parsed.Activity.DistanceMeters, d)
				}
			}
			prev = &p
		}
	}
	if !hasStart {
		return nil, fmt.Errorf("no valid track point timestamps found")
	}
	parsed.Activity.StartTimestamp = startTime
	end := endTime
	parsed.Activity.EndTimestamp = &end
	parsed.Activity.DurationSeconds = floatSecs(end.Sub(startTime))

	return parsed, nil
}

func addGPXPointSamples(p *Parsed, t time.Time, pnt gpxPoint) {
	actID := p.Activity.ID
	if el := parseFloat64(pnt.Ele); el != nil {
		p.Samples = append(p.Samples, sample(actID, domain.MetricElevation, t, *el))
	}
	if hr := parseFloat64(pnt.HR); hr != nil {
		p.Samples = append(p.Samples, sample(actID, domain.MetricHeartRate, t, *hr))
	}
	if s := parseFloat64(pnt.Speed); s != nil {
		p.Samples = append(p.Samples, sample(actID, domain.MetricSpeed, t, *s))
	}
	if lat := parseFloat64(pnt.Lat); lat != nil {
		if lon := parseFloat64(pnt.Lon); lon != nil {
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
}

func trackNames(tracks []gpxTrack) []string {
	names := make([]string, 0, len(tracks))
	for _, tr := range tracks {
		names = append(names, strings.TrimSpace(tr.Name))
	}
	return names
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func floatSecs(d time.Duration) *float64 {
	if d <= 0 {
		return nil
	}
	s := d.Seconds()
	return &s
}

func addFloat(cur *float64, v float64) *float64 {
	if cur == nil {
		return &v
	}
	*cur += v
	return cur
}

// haversine returns the great-circle distance in meters between two GPX points.
func haversine(a, b *gpxPoint) float64 {
	const r = 6371000.0
	lat1, lon1 := toRad(a.Lat), toRad(a.Lon)
	lat2, lon2 := toRad(b.Lat), toRad(b.Lon)
	dLat := lat2 - lat1
	dLon := lon2 - lon1
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

func toRad(v string) float64 {
	d := parseFloat64(v)
	if d == nil {
		return 0
	}
	return *d * math.Pi / 180
}

// normalizeGPXSport maps an optional GPX track type to a slug.
func normalizeGPXSport(t string) domain.Sport {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "run", "running":
		return domain.Sport("run")
	case "ride", "cycling", "biking":
		return domain.Sport("ride")
	case "walk", "walking", "hike", "hiking":
		return domain.Sport("walk")
	default:
		return domain.Sport("other")
	}
}
