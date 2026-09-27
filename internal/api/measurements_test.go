package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/service"
)

func TestMeasuredRunHTTPUsesOperatorEvidence(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow A","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var response struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response.Data.Operation.ID
	if id == "" {
		t.Fatal("missing ID")
	}
	stamp := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	input := `{"kind":"wave_time","source_id":"probe-a","value":0.24,"recorded_at":"` + stamp + `"}`
	recorded := httptest.NewRecorder()
	server.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+string(id)+"/evidence", strings.NewReader(input)))
	if recorded.Code != http.StatusOK {
		t.Fatalf("record: %d %s", recorded.Code, recorded.Body)
	}
	analysis := httptest.NewRecorder()
	server.ServeHTTP(analysis, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+string(id)+"/analysis", nil))
	if analysis.Code != http.StatusOK || !strings.Contains(analysis.Body.String(), `"mean":0.24`) {
		t.Fatalf("analysis: %d %s", analysis.Code, analysis.Body)
	}
	reloaded, err := service.OpenRuntime(t.TempDir())
	if err != nil || len(reloaded.MeasuredRuns()) != 0 {
		t.Fatal("archive separation failed", err)
	}
}

func TestMeasuredRunAnalysisExposesSpectraOnlyWhenEvidenceSuffices(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()

	create := func(name string) string {
		created := httptest.NewRecorder()
		body := `{"name":"` + name + `","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`
		server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(body)))
		if created.Code != http.StatusOK {
			t.Fatalf("create: %d %s", created.Code, created.Body)
		}
		var response struct {
			Data service.MeasuredRun `json:"data"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return string(response.Data.Operation.ID)
	}
	record := func(id string, k int, value float64, stamp time.Time) {
		input := `{"kind":"wave_time","source_id":"probe-a","value":` +
			strconv.FormatFloat(value, 'f', -1, 64) +
			`,"recorded_at":"` + stamp.Format(time.RFC3339Nano) + `"}`
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(input)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("record %d: %d %s", k, recorder.Code, recorder.Body)
		}
	}
	analyze := func(id string) string {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+id+"/analysis", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("analysis: %d %s", recorder.Code, recorder.Body)
		}
		return recorder.Body.String()
	}

	// Continuous run: 128 frames of a 1 Hz sine sampled at 50 Hz.
	dense := create("tow dense")
	t0 := time.Now().Add(-time.Hour).UTC()
	for k := 0; k < 128; k++ {
		value := 0.1 * math.Sin(2*math.Pi*float64(k)/50)
		record(dense, k, value, t0.Add(time.Duration(k)*20*time.Millisecond))
	}
	denseBody := analyze(dense)
	if !strings.Contains(denseBody, `"spectra"`) || !strings.Contains(denseBody, `"frequency_hz"`) {
		t.Fatalf("dense analysis missing spectral data: %s", denseBody)
	}
	if !strings.Contains(denseBody, `"status":"ok"`) {
		t.Fatalf("dense analysis not ok: %s", denseBody)
	}

	// Sparse run: three frames cannot support a spectrum.
	sparse := create("tow sparse")
	for k := 0; k < 3; k++ {
		record(sparse, k, 0.1, t0.Add(time.Duration(k)*20*time.Millisecond))
	}
	sparseBody := analyze(sparse)
	if strings.Contains(sparseBody, `"frequency_hz"`) {
		t.Fatalf("sparse analysis leaked a spectrum: %s", sparseBody)
	}
	if !strings.Contains(sparseBody, `"too_few_frames"`) {
		t.Fatalf("sparse analysis missing the insufficiency reason: %s", sparseBody)
	}
}
