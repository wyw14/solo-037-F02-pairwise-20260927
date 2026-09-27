package service

import (
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func TestOperatorMeasurementsPersistAndAnalyzeSuppliedValues(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test A", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	for _, input := range []MeasurementInput{
		{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 0.1, RecordedAt: at},
		{Kind: model.EvidenceWaveTime, SourceID: "right", Value: 0.3, RecordedAt: at.Add(time.Second)},
		{Kind: model.EvidenceForce, SourceID: "load-cell", Value: 12, RecordedAt: at},
	} {
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := loaded.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Wave.Count != 2 || math.Abs(analysis.Wave.Mean-0.2) > 1e-9 || analysis.ForceCount != 1 || analysis.ForceMean != 12 {
		t.Fatalf("analysis did not reflect supplied values after reload: %+v", analysis)
	}
	if len(loaded.List()) != 1 || len(loaded.List()[0].Evidence) != 3 || !loaded.List()[0].Evidence[0].Durable {
		t.Fatalf("persisted evidence missing: %+v", loaded.List())
	}
}

func TestOperatorMeasurementRejectsInvalidAndUnknownRun(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test B", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	input := MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 1, RecordedAt: time.Now().UTC()}
	if _, err := a.Record("missing", input); err == nil {
		t.Fatal("unknown run accepted")
	}
	input.Value = math.NaN()
	if _, err := a.Record(run.Operation.ID, input); err == nil {
		t.Fatal("NaN accepted")
	}
	if len(a.List()[0].Evidence) != 0 {
		t.Fatal("rejected values changed archive")
	}
}

func TestOperatorMeasurementAnalysisIncludesSpectraForContinuousFrames(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("tow sine", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour).UTC()
	record := func(source string, k int, value float64) {
		input := MeasurementInput{
			Kind:       model.EvidenceWaveTime,
			SourceID:   source,
			Value:      value,
			RecordedAt: t0.Add(time.Duration(k) * 20 * time.Millisecond),
		}
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	for k := 0; k < 128; k++ {
		record("probe-a", k, 0.1*math.Sin(2*math.Pi*float64(k)/50))
	}
	for k := 0; k < 12; k++ {
		record("probe-b", k, 0.1*math.Sin(2*math.Pi*float64(k)/50))
	}
	analysis, err := a.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Spectra) != 2 {
		t.Fatalf("spectra = %d, want 2 sources", len(analysis.Spectra))
	}
	if analysis.Spectra[0].SourceID != "probe-a" || analysis.Spectra[0].Status != "ok" {
		t.Fatalf("probe-a spectrum = %+v", analysis.Spectra[0])
	}
	peak := analysis.Spectra[0].Segments[0].Peak
	if peak == nil || math.Abs(peak.FrequencyHz-1.0) > 0.05 {
		t.Fatalf("probe-a peak = %+v", peak)
	}
	if analysis.Spectra[1].Status != "insufficient" || analysis.Spectra[1].Segments[0].Peak != nil {
		t.Fatalf("probe-b must not produce a spectrum: %+v", analysis.Spectra[1])
	}
}

func TestOperatorMeasurementAnalysisSparseFramesHasNoSpectralData(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("tow sparse", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour).UTC()
	for k := 0; k < 3; k++ {
		input := MeasurementInput{
			Kind:       model.EvidenceWaveTime,
			SourceID:   "probe-a",
			Value:      0.1 * math.Sin(float64(k)),
			RecordedAt: t0.Add(time.Duration(k) * 20 * time.Millisecond),
		}
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	analysis, err := a.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Spectra) != 1 {
		t.Fatalf("spectra = %d", len(analysis.Spectra))
	}
	source := analysis.Spectra[0]
	if source.Status != "insufficient" || source.Reason != "too_few_frames" {
		t.Fatalf("sparse spectrum = %+v", source)
	}
	if source.Segments[0].Peak != nil || source.Segments[0].Power != nil {
		t.Fatalf("insufficient spectrum leaked spectral data: %+v", source.Segments[0])
	}
}
