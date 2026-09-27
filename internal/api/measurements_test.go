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

func TestMeasuredRunAnalysisReportsSpectrum(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	create := func() string {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow B","model_name":"hull B","section":"section-a","carriage":"carriage-1"}`)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("create: %d %s", recorder.Code, recorder.Body)
		}
		var response struct {
			Data service.MeasuredRun `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return string(response.Data.Operation.ID)
	}
	analyze := func(id string) string {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+id+"/analysis", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("analysis: %d %s", recorder.Code, recorder.Body)
		}
		return recorder.Body.String()
	}
	base := time.Now().Add(-time.Hour).UTC()
	record := func(id string, index int) {
		stamp := base.Add(time.Duration(index) * 50 * time.Millisecond).Format(time.RFC3339Nano)
		value := 0.1 * math.Sin(2*math.Pi*0.5*float64(index)*0.05)
		body := `{"kind":"wave_time","source_id":"probe-a","value":` + strconv.FormatFloat(value, 'g', -1, 64) + `,"recorded_at":"` + stamp + `"}`
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("record: %d %s", recorder.Code, recorder.Body)
		}
	}
	continuous := create()
	for index := 0; index < 32; index++ {
		record(continuous, index)
	}
	if body := analyze(continuous); !strings.Contains(body, `"peak_hz":0.625`) {
		t.Fatalf("continuous series should report the 0.5 Hz wave near bin 0.625 Hz: %s", body)
	}
	sparse := create()
	record(sparse, 0)
	if body := analyze(sparse); !strings.Contains(body, `"status":"insufficient"`) || strings.Contains(body, `"bins"`) {
		t.Fatalf("single frame must not produce a spectrum: %s", body)
	}
}
