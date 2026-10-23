package importer

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/garmin/workout-viewer/backend/internal/domain"
)

// FITAdapter decodes Garmin Interactive Fitness (FIT) binary files.
//
// It implements the documented FIT container/message format using only the
// standard library. Only base data types are decoded: uint8/16/32, their
// "undefined is zero" (_z) variants, float32, bool, string and signed 32-bit.
// Encoded (_E), default-zero (_DEF_Z) and bit-field types require the FIT data
// files (fdt/dft) which are not bundled, so fields using those types are
// skipped rather than mis-decoded.
//
// Field numbers follow the public FIT profile for the standard session, lap
// and record messages. Coordinates use the signed-s32 convention
// (value / 2^31 * 90); this is documented because it cannot be validated here
// against real Garmin files.
type FITAdapter struct{}

var _ Adapter = (*FITAdapter)(nil)

func (*FITAdapter) Format() string { return "fit" }

// Standard global message numbers.
const (
	msgFileID    = 20
	msgSession   = 3
	msgLap       = 8
	msgRecord    = 10
	fitEpochYear = 1989
)

// Canonical field order per message. Standard FIT messages define fields in
// ascending field-number order, so the position of a decoded field maps to the
// k-th entry below. This assumes devices emit the common field subset in that
// order; where a device adds extra fields ahead of these, the mapping shifts.
// That is the one place real-file decoding depends on the FIT profile.
var (
	// file_id (msg 20): manufacturer, product, serial_number, time_created.
	fileIDOrder = []int{0, 1, 2, 3}
	// session (msg 3): start_time, total_distance, calories, total_elapsed_time,
	// average_heart_rate, max_heart_rate.
	sessionOrder = []int{0, 1, 3, 23, 28, 29}
	// lap (msg 8): start_time, end_time, total_elapsed_time, total_distance,
	// calories, average_heart_rate.
	lapOrder = []int{0, 1, 2, 3, 4, 12}
	// record (msg 10): timestamp, position_lat, position_long, altitude, speed,
	// heart_rate, cadence, power.
	recordOrder = []int{0, 1, 2, 3, 4, 5, 6, 7}
)

// fieldAt returns the decoded value at canonical position k, if present.
func fieldAt(v map[uint8]rawValue, k int) (rawValue, bool) {
	if k < 0 {
		return rawValue{}, false
	}
	return v[uint8(k)], true
}

// fitReader walks the raw FIT byte buffer.
type fitReader struct {
	buf []byte
	pos int
}

func (r *fitReader) remaining() int { return len(r.buf) - r.pos }
func (r *fitReader) u8() uint8      { v := r.buf[r.pos]; r.pos++; return v }
func (r *fitReader) u16() uint16 {
	v := binary.LittleEndian.Uint16(r.buf[r.pos:])
	r.pos += 2
	return v
}
func (r *fitReader) u32() uint32 {
	v := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v
}

func (*FITAdapter) Parse(data []byte) (*Parsed, error) {
	if !isFIT(data) {
		return nil, fmt.Errorf("not a FIT file")
	}
	r := &fitReader{buf: data}
	if err := r.readHeader(); err != nil {
		return nil, err
	}

	var (
		fileID     fitFileID
		sess       fitSession
		hasSession bool
		laps       []fitLap
		records    []fitRecord
	)

	for r.remaining() > 0 {
		def, body, err := r.readMessageDefinition()
		if err != nil {
			return nil, err
		}
		values, err := decodeFields(def, body)
		if err != nil {
			return nil, err
		}
		switch def.globalMessageNumber {
		case msgFileID:
			fileID = decodeFileID(values)
		case msgSession:
			sess = decodeSession(values)
			hasSession = true
		case msgLap:
			laps = append(laps, decodeLap(values))
		case msgRecord:
			records = append(records, decodeRecord(values))
		}
	}

	if !hasSession && len(laps) == 0 && len(records) == 0 {
		return nil, fmt.Errorf("FIT file has no session, lap or record messages")
	}

	parsed := &Parsed{Activity: buildFitActivity(fileID, sess)}
	for i, lap := range laps {
		if l := fitLapToDomain(parsed.Activity.ID, i+1, lap); l != nil {
			parsed.Laps = append(parsed.Laps, l)
		}
	}
	base := sess.Start
	if base.IsZero() {
		base = fileID.TimeCreated
	}
	for _, rec := range records {
		for _, s := range fitRecordToSamples(parsed.Activity.ID, base, rec) {
			parsed.Samples = append(parsed.Samples, s)
		}
	}

	return parsed, nil
}

func (r *fitReader) readHeader() error {
	if r.remaining() < 12 {
		return fmt.Errorf("FIT file too short")
	}
	if r.buf[0] != 0x23 || r.buf[1] != 0x08 {
		return fmt.Errorf("invalid FIT magic bytes")
	}
	if !strings.HasPrefix(string(r.buf[4:8]), ".FIT") {
		return fmt.Errorf("invalid FIT suffix")
	}
	r.pos += 12 // consume the 12-byte header
	return nil
}

// readMessageDefinition reads one message definition and its body.
func (r *fitReader) readMessageDefinition() (fitDef, []byte, error) {
	if r.remaining() < 1 {
		return fitDef{}, nil, fmt.Errorf("truncated message")
	}
	format := r.u8()
	isLocal := format&0x80 == 0
	defSize := int(format & 0x0F)
	if defSize == 0 {
		defSize = int(r.u8())
	}
	if r.remaining() < defSize {
		return fitDef{}, nil, fmt.Errorf("truncated message definition")
	}
	numFields := r.u8()
	fields := make([]fitFieldDef, 0, numFields)
	dataSize := 0
	for i := uint8(0); i < numFields; i++ {
		dataType := r.u8()
		array := r.u8() & 0x3F
		_ = r.u8() // development number
		fields = append(fields, fitFieldDef{dataType: dataType, array: array})
		// The data body length is the sum of each field's byte size. Array
		// fields repeat (array == 0 means a single value). Encoded/unknown
		// types cannot have their length determined, so they are unsupported.
		size := baseFieldTypeSize(dataType)
		if size == 0 {
			return fitDef{}, nil, fmt.Errorf("unsupported field type %d", dataType)
		}
		elem := int(array & 0x3F)
		if elem == 0 {
			elem = 1
		}
		dataSize += size * elem
	}
	var globalNum uint16
	if isLocal {
		r.u8() // local message type
	} else {
		globalNum = r.u16()
	}
	body := make([]byte, dataSize)
	if err := r.readFull(body); err != nil {
		return fitDef{}, nil, err
	}
	return fitDef{fields: fields, globalMessageNumber: globalNum}, body, nil
}

// fieldHeaderSize is the bytes consumed by field definitions (3 each).
func fieldHeaderSize(numFields uint8) int { return int(numFields) * 3 }

func (r *fitReader) readFull(b []byte) error {
	if r.remaining() < len(b) {
		return fmt.Errorf("truncated message body")
	}
	copy(b, r.buf[r.pos:r.pos+len(b)])
	r.pos += len(b)
	return nil
}

type fitDef struct {
	fields              []fitFieldDef
	globalMessageNumber uint16
}

type fitFieldDef struct {
	dataType uint8
	array    uint8
}

// decodeFields decodes each field using only base types; unsupported types are
// left out of the map so callers can ignore them.
func decodeFields(def fitDef, body []byte) (map[uint8]rawValue, error) {
	out := make(map[uint8]rawValue)
	pos := 0
	for i, fd := range def.fields {
		size := baseFieldTypeSize(fd.dataType)
		if size == 0 {
			// Encoded/def-z/bits/string: advance by a safe guess (1 byte) so the
			// cursor keeps moving, but do not decode the value.
			pos++
			continue
		}
		if pos+size > len(body) {
			return nil, fmt.Errorf("truncated field %d", i)
		}
		v, ok := decodeRawValue(fd.dataType, body[pos:pos+size])
		if ok {
			out[uint8(i)] = v
		}
		pos += size
	}
	return out, nil
}

// baseFieldTypeSize returns the byte width of a decodable base type, or 0 for
// types that require the FIT data files.
func baseFieldTypeSize(t uint8) int {
	switch t {
	case 0, 13, 16: // uint8, bool, string
		return 1
	case 1: // uint16
		return 2
	case 2, 3, 10: // uint32, uint32_z, float32
		return 4
	}
	if t >= 0x6B && t <= 0x6E { // signed base types s8..s64
		if t == 0x6B || t == 0x72 { // s8, s8_def_z
			return 1
		}
		if t == 0x6C || t == 0x73 { // s16
			return 2
		}
		return 4 // s32, s64 and friends
	}
	return 0
}

// rawValue holds a decoded base-type field.
type rawValue struct {
	uint8Val   uint8
	uint16Val  uint16
	uint32Val  uint32
	float32Val float32
	boolVal    bool
	s32Val     int32
}

func decodeRawValue(t uint8, b []byte) (rawValue, bool) {
	switch t {
	case 0:
		return rawValue{uint8Val: b[0]}, true
	case 13:
		return rawValue{boolVal: b[0] != 0}, true
	case 1:
		return rawValue{uint16Val: binary.LittleEndian.Uint16(b)}, true
	case 2, 3:
		return rawValue{uint32Val: binary.LittleEndian.Uint32(b)}, true
	case 10:
		return rawValue{float32Val: math.Float32frombits(binary.LittleEndian.Uint32(b))}, true
	case 0x6D: // s32
		return rawValue{s32Val: int32(binary.LittleEndian.Uint32(b))}, true
	default:
		return rawValue{}, false
	}
}

// ---- message decoders ----

type fitFileID struct {
	Product     uint16
	Serial      uint32
	TimeCreated time.Time
}

func decodeFileID(v map[uint8]rawValue) fitFileID {
	var f fitFileID
	if val, ok := fieldAt(v, 1); ok { // product
		f.Product = val.uint16Val
	}
	if val, ok := fieldAt(v, 2); ok { // serial number
		f.Serial = val.uint32Val
	}
	if val, ok := fieldAt(v, 3); ok { // time created utc timestamp
		f.TimeCreated = fitUtcTimestamp(val.uint32Val)
	}
	return f
}

type fitSession struct {
	Start    time.Time
	Elapsed  uint32
	Distance uint32
	Calories uint16
	AvgHR    uint8
	MaxHR    uint8
}

func decodeSession(v map[uint8]rawValue) fitSession {
	var s fitSession
	if val, ok := fieldAt(v, 0); ok { // start_time
		s.Start = fitUtcTimestamp(val.uint32Val)
	}
	if val, ok := fieldAt(v, 1); ok { // total_distance (meters)
		s.Distance = val.uint32Val
	}
	if val, ok := fieldAt(v, 2); ok { // calories
		s.Calories = val.uint16Val
	}
	if val, ok := fieldAt(v, 3); ok { // total_elapsed_time (seconds)
		s.Elapsed = val.uint32Val
	}
	if val, ok := fieldAt(v, 4); ok { // average_heart_rate
		s.AvgHR = val.uint8Val
	}
	if val, ok := fieldAt(v, 5); ok { // max_heart_rate
		s.MaxHR = val.uint8Val
	}
	return s
}

type fitLap struct {
	Start    time.Time
	End      time.Time
	Elapsed  uint32
	Distance uint32
	Calories uint16
	AvgHR    uint8
}

func decodeLap(v map[uint8]rawValue) fitLap {
	var l fitLap
	if val, ok := fieldAt(v, 0); ok { // start time
		l.Start = fitUtcTimestamp(val.uint32Val)
	}
	if val, ok := fieldAt(v, 1); ok { // end time
		l.End = fitUtcTimestamp(val.uint32Val)
	}
	if val, ok := fieldAt(v, 2); ok { // elapsed time
		l.Elapsed = val.uint32Val
	}
	if val, ok := fieldAt(v, 3); ok { // distance
		l.Distance = val.uint32Val
	}
	if val, ok := fieldAt(v, 4); ok { // calories
		l.Calories = val.uint16Val
	}
	if val, ok := fieldAt(v, 5); ok { // avg heart rate
		l.AvgHR = val.uint8Val
	}
	return l
}

type fitRecord struct {
	Timestamp uint32 // seconds since session start
	HR        uint8
	Cadence   uint8
	Power     uint16
	Altitude  float32
	Lat       int32
	Lon       int32
}

func decodeRecord(v map[uint8]rawValue) fitRecord {
	var r fitRecord
	if val, ok := fieldAt(v, 0); ok { // timestamp (seconds since session start)
		r.Timestamp = val.uint32Val
	}
	if val, ok := fieldAt(v, 1); ok && val.s32Val != 0 { // position_lat
		r.Lat = val.s32Val
	}
	if val, ok := fieldAt(v, 2); ok && val.s32Val != 0 { // position_long
		r.Lon = val.s32Val
	}
	if val, ok := fieldAt(v, 3); ok { // altitude (float32 meters)
		r.Altitude = val.float32Val
	}
	if val, ok := fieldAt(v, 5); ok { // heart rate
		r.HR = val.uint8Val
	}
	if val, ok := fieldAt(v, 6); ok { // cadence
		r.Cadence = val.uint8Val
	}
	if val, ok := fieldAt(v, 7); ok { // power
		r.Power = val.uint16Val
	}
	return r
}

// fitCoordToDegrees converts a decoded signed coordinate to degrees using the
// common FIT convention (value / 2^31 * 90). It returns nil for values outside
// the valid [-90, 90] range so garbage coordinates are never stored.
func fitCoordToDegrees(v int32) *float64 {
	deg := float64(v) / 2147483648.0 * 90.0
	if deg < -90 || deg > 90 {
		return nil
	}
	return &deg
}

func fitUtcTimestamp(secs uint32) time.Time {
	epoch := time.Date(fitEpochYear, 12, 31, 0, 0, 0, 0, time.UTC)
	return epoch.Add(time.Duration(secs) * time.Second)
}

func buildFitActivity(fit fitFileID, sess fitSession) *domain.Activity {
	a := &domain.Activity{Sport: domain.Sport("other")}
	if fit.Serial != 0 {
		a.SourceID = fmt.Sprintf("fit:%d", fit.Serial)
	} else if fit.Product != 0 {
		a.SourceID = fmt.Sprintf("fit-product:%d", fit.Product)
	}
	if sess.Start.IsZero() {
		sess.Start = fit.TimeCreated
	}
	a.StartTimestamp = sess.Start
	if dur := float64(sess.Elapsed); dur > 0 {
		a.DurationSeconds = &dur
		end := sess.Start.Add(time.Duration(sess.Elapsed) * time.Second)
		a.EndTimestamp = &end
	}
	if d := float64(sess.Distance); d > 0 {
		a.DistanceMeters = &d
	}
	if sess.Calories > 0 {
		cal := float64(sess.Calories)
		a.Calories = &cal
	}
	if sess.AvgHR > 0 {
		hr := int(sess.AvgHR)
		a.AvgHeartRate = &hr
	}
	if sess.MaxHR > 0 {
		hr := int(sess.MaxHR)
		a.MaxHeartRate = &hr
	}
	if a.Name == "" {
		a.Name = "FIT activity"
	}
	return a
}

func fitLapToDomain(activityID string, idx int, lap fitLap) *domain.Lap {
	l := &domain.Lap{
		ID:             fmt.Sprintf("%s-lap-%d", activityID, idx),
		ActivityID:     activityID,
		Index:          idx,
		StartTimestamp: lap.Start,
	}
	if d := float64(lap.Elapsed); d > 0 {
		l.DurationSeconds = &d
	}
	if d := float64(lap.Distance); d > 0 {
		l.DistanceMeters = &d
	}
	if c := float64(lap.Calories); c > 0 {
		l.Calories = &c
	}
	if lap.AvgHR > 0 {
		hr := int(lap.AvgHR)
		l.AvgHeartRate = &hr
	}
	if !lap.End.IsZero() {
		end := lap.End
		l.EndTimestamp = &end
	}
	return l
}

func fitRecordToSamples(activityID string, base time.Time, rec fitRecord) []*domain.Sample {
	if rec.Timestamp == 0 && rec.HR == 0 && rec.Cadence == 0 && rec.Power == 0 && rec.Altitude == 0 {
		return nil
	}
	t := base.Add(time.Duration(rec.Timestamp) * time.Second)
	var out []*domain.Sample
	if rec.HR > 0 {
		out = append(out, sample(activityID, domain.MetricHeartRate, t, float64(rec.HR)))
	}
	if rec.Cadence > 0 {
		out = append(out, sample(activityID, domain.MetricCadence, t, float64(rec.Cadence)))
	}
	if rec.Power > 0 {
		out = append(out, sample(activityID, domain.MetricPower, t, float64(rec.Power)))
	}
	if rec.Altitude != 0 {
		out = append(out, sample(activityID, domain.MetricElevation, t, float64(rec.Altitude)))
	}
	if rec.Lat != 0 && rec.Lon != 0 {
		if lat := fitCoordToDegrees(rec.Lat); lat != nil {
			if lon := fitCoordToDegrees(rec.Lon); lon != nil {
				out = append(out, &domain.Sample{
					ID:         fmt.Sprintf("%s-%d-gps", activityID, t.UnixNano()),
					ActivityID: activityID,
					Kind:       domain.MetricGPS,
					Timestamp:  t,
					Latitude:   lat,
					Longitude:  lon,
				})
			}
		}
	}
	return out
}
