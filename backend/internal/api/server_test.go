package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/garmin/workout-viewer/backend/internal/domain"
	"github.com/garmin/workout-viewer/backend/internal/persistence"
)

// fakeRepo is an in-memory ActivityRepository used to exercise handlers without
// a database.
type fakeRepo struct {
	page     *domain.ActivityPage
	act      *domain.Activity
	samples  []*domain.Sample
	laps     []*domain.Lap
	metrics  *domain.MetricList
	summary  *domain.Summary
	sports   []*domain.SportDistribution
	listErr  error
	notFound bool
}

func (f *fakeRepo) Ping(context.Context) error { return nil }
func (f *fakeRepo) UpsertActivity(context.Context, *domain.Activity, []*domain.Lap, []*domain.Sample) error {
	return nil
}
func (f *fakeRepo) GetActivity(context.Context, string) (*domain.Activity, error) {
	if f.notFound {
		return nil, persistence.ErrNotFound
	}
	return f.act, nil
}
func (f *fakeRepo) ListActivities(context.Context, persistence.Filter, domain.Pagination) (*domain.ActivityPage, error) {
	return f.page, f.listErr
}
func (f *fakeRepo) GetSamples(context.Context, string, *domain.MetricKind) ([]*domain.Sample, error) {
	return f.samples, nil
}
func (f *fakeRepo) GetLaps(context.Context, string) ([]*domain.Lap, error) { return f.laps, nil }
func (f *fakeRepo) GetMetrics(context.Context, string) (*domain.MetricList, error) {
	return f.metrics, nil
}
func (f *fakeRepo) Summary(context.Context, *persistence.Window) (*domain.Summary, error) {
	return f.summary, nil
}
func (f *fakeRepo) SportDistribution(context.Context) ([]*domain.SportDistribution, error) {
	return f.sports, nil
}

func newTestServer(t *testing.T, repo persistence.ActivityRepository) *httptest.Server {
	t.Helper()
	s := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), CORSConfig{Origins: []string{"*"}})
	return httptest.NewServer(s.Handler)
}

func TestHealthz(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestListActivitiesValidation(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)
	defer srv.Close()

	for _, q := range []string{"limit=abc", "limit=0", "limit=999", "offset=-1", "start=not-a-date"} {
		resp, err := http.Get(srv.URL + "/api/v1/activities?" + q)
		if err != nil {
			t.Fatalf("GET %s: %v", q, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, resp.StatusCode)
		}
	}
}

func TestListActivities(t *testing.T) {
	start := time.Now().UTC()
	repo := &fakeRepo{page: &domain.ActivityPage{
		Total: 1,
		Items: []*domain.Activity{{ID: "a1", SourceID: "s1", StartTimestamp: start}},
	}}
	srv := newTestServer(t, repo)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/activities?limit=10")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var page domain.ActivityPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestGetActivityNotFound(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	srv := newTestServer(t, repo)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/activities/missing")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSummary(t *testing.T) {
	dist := 1234.5
	repo := &fakeRepo{summary: &domain.Summary{ActivityCount: 2, DistanceMeters: &dist}}
	srv := newTestServer(t, repo)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/summary")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var sum domain.Summary
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sum.ActivityCount != 2 || sum.DistanceMeters == nil || *sum.DistanceMeters != 1234.5 {
		t.Fatalf("unexpected summary: %+v", sum)
	}
}

func TestCORSHeader(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/healthz", nil)
	req.Header.Set("Origin", "https://example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" && got != "https://example.com" {
		t.Fatalf("CORS header = %q, want * or reflected origin", got)
	}
}

// dupRepo records how many activities were upserted so the duplicate path can be
// exercised: the first import succeeds, the second reports a duplicate.
type dupRepo struct {
	fakeRepo
	calls int
}

func (d *dupRepo) UpsertActivity(context.Context, *domain.Activity, []*domain.Lap, []*domain.Sample) error {
	d.calls++
	if d.calls == 1 {
		return nil
	}
	return persistence.ErrDuplicateSource
}

// uploadMultipart builds a multipart/form-data POST body with a single "file"
// field and returns the request ready for http.DefaultClient.
func uploadMultipart(t *testing.T, srvURL, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srvURL+"/api/v1/activities", &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

const tcxFixture = `<?xml version="1.0"?>
<TrainingCenterDatabase>
  <Activities>
  <Activity Id="2024-05-01T08:00:00Z">
    <Name>Run</Name>
    <Sport>Run</Sport>
    <Lap StartTime="2024-05-01T08:00:00Z">
      <TotalTime>600</TotalTime>
      <Distance>2000</Distance>
      <Calories>300</Calories>
    </Lap>
    <Track>
      <TrackPoint><Time>2024-05-01T08:00:00Z</Time><Cadence>85</Cadence></TrackPoint>
      <TrackPoint><Time>2024-05-01T08:00:10Z</Time><Cadence>87</Cadence></TrackPoint>
    </Track>
  </Activity>
  </Activities>
</TrainingCenterDatabase>`

func TestUploadActivity(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)
	defer srv.Close()

	req := uploadMultipart(t, srv.URL, "run.tcx", []byte(tcxFixture))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d (%s), want 201", resp.StatusCode, body)
	}

	var out struct {
		Format      string `json:"format"`
		SourceID    string `json:"sourceID"`
		ActivityID  string `json:"activityID"`
		LapCount    int    `json:"lapCount"`
		SampleCount int    `json:"sampleCount"`
		Duplicate   bool   `json:"duplicate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Format != "tcx" || out.LapCount != 1 || out.SampleCount == 0 || out.Duplicate {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestUploadActivityDuplicate(t *testing.T) {
	repo := &dupRepo{}
	srv := newTestServer(t, repo)
	defer srv.Close()

	for i := 0; i < 2; i++ {
		req := uploadMultipart(t, srv.URL, "run.tcx", []byte(tcxFixture))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST #%d: %v", i, err)
		}
		resp.Body.Close()
		// First import creates (201), the duplicate is accepted as 200.
		want := http.StatusCreated
		if i == 1 {
			want = http.StatusOK
		}
		if resp.StatusCode != want {
			t.Fatalf("import #%d: status = %d, want %d", i, resp.StatusCode, want)
		}
	}
}

func TestUploadActivityNoFile(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/activities", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "multipart/form-data")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadActivityUnsupportedFormat(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)
	defer srv.Close()

	req := uploadMultipart(t, srv.URL, "notes.txt", []byte("not a workout file"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", resp.StatusCode)
	}
}
