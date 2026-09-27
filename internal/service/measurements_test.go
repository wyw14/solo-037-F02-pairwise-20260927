package service

import (
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/probe"
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

func TestOperatorMeasurementSpectrumFromContinuousFrames(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test C", "hull Y", "section-b", "carriage-2")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Hour).UTC()
	for index := 0; index < 32; index++ {
		value := 0.1 * math.Sin(2*math.Pi*0.5*float64(index)*0.05)
		input := MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: value, RecordedAt: start.Add(time.Duration(index) * 50 * time.Millisecond)}
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	analysis, err := a.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Wave.Count != 32 {
		t.Fatalf("wave stats lost raw frames: %+v", analysis.Wave)
	}
	if len(analysis.Spectrum.Spectra) != 1 {
		t.Fatalf("spectra %+v", analysis.Spectrum.Spectra)
	}
	spectrum := analysis.Spectrum.Spectra[0]
	if spectrum.Status != probe.SpectrumOK {
		t.Fatalf("spectrum %+v", spectrum)
	}
	if math.Abs(spectrum.PeakHz-0.5) > spectrum.ResolutionHz {
		t.Fatalf("peak %v Hz not within resolution %v of 0.5 Hz", spectrum.PeakHz, spectrum.ResolutionHz)
	}
	if !spectrum.WindowFirst.Equal(start) || spectrum.Frames != 32 {
		t.Fatalf("window %+v does not cover the recorded series", spectrum)
	}
	loaded, ok := a.Get(run.Operation.ID)
	if !ok || len(loaded.Evidence) != 32 {
		t.Fatalf("raw wave evidence not reviewable: %+v", loaded)
	}
}

func TestOperatorMeasurementSpectrumInsufficientForSparseFrames(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test D", "hull Y", "section-b", "carriage-2")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Hour).UTC()
	for index := 0; index < 3; index++ {
		input := MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.1, RecordedAt: start.Add(time.Duration(index) * 50 * time.Millisecond)}
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	analysis, err := a.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Spectrum.Spectra) != 1 {
		t.Fatalf("spectra %+v", analysis.Spectrum.Spectra)
	}
	spectrum := analysis.Spectrum.Spectra[0]
	if spectrum.Status != probe.SpectrumInsufficient || spectrum.Reason == "" {
		t.Fatalf("sparse frames should not produce a spectrum: %+v", spectrum)
	}
	if len(spectrum.Bins) != 0 || spectrum.PeakHz != 0 {
		t.Fatalf("insufficient spectrum carries bins/peak: %+v", spectrum)
	}
}

func TestOperatorMeasurementRejectsInvalidAndUnknownRun(t *testing.T) {	a, err := OpenMeasurementArchive(t.TempDir())
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
