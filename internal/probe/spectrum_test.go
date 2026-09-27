package probe

import (
    "math"
    "strings"
    "testing"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

var spectrumBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// sineSeries returns frames of a sine wave sampled every dt, used to build
// continuous and broken operator series in tests.
func sineSeries(source string, start time.Time, dt time.Duration, frames int, amplitude float64, hz float64) []model.Evidence {
    samples := make([]model.Evidence, 0, frames)
    for index := 0; index < frames; index++ {
        at := start.Add(time.Duration(index) * dt)
        value := amplitude * math.Sin(2*math.Pi*hz*float64(index)*dt.Seconds())
        samples = append(samples, model.NewEvidence("run-1", "rev-1", source, model.EvidenceWaveTime, value, at))
    }
    return samples
}

func onlySpectrum(t *testing.T, report SpectralReport) Spectrum {
    t.Helper()
    if len(report.Spectra) != 1 {
        t.Fatalf("expected one spectrum, got %+v", report.Spectra)
    }
    return report.Spectra[0]
}

func TestAnalyzeSpectrumFindsPeakFrequency(t *testing.T) {
    start := spectrumBase
    samples := sineSeries("probe-a", start, 50*time.Millisecond, 64, 0.12, 0.8)
    report := AnalyzeSpectrum(samples)
    spectrum := onlySpectrum(t, report)
    if spectrum.Status != SpectrumOK {
        t.Fatalf("status %q reason %q", spectrum.Status, spectrum.Reason)
    }
    if math.Abs(spectrum.PeakHz-0.8) > spectrum.ResolutionHz {
        t.Fatalf("peak %v Hz not within resolution %v of 0.8 Hz", spectrum.PeakHz, spectrum.ResolutionHz)
    }
    if math.Abs(spectrum.SampleHz-20) > 0.1 {
        t.Fatalf("sample rate %v, want 20 Hz", spectrum.SampleHz)
    }
    if spectrum.Frames != 64 {
        t.Fatalf("frames %d, want 64", spectrum.Frames)
    }
    if !spectrum.WindowFirst.Equal(start) || !spectrum.WindowLast.Equal(start.Add(63*50*time.Millisecond)) {
        t.Fatalf("window %v..%v does not cover the series", spectrum.WindowFirst, spectrum.WindowLast)
    }
    if spectrum.PeakAmplitude < 0.08 || spectrum.PeakAmplitude > 0.16 {
        t.Fatalf("peak amplitude %v far from sine amplitude 0.12", spectrum.PeakAmplitude)
    }
    if len(spectrum.Bins) != 32 {
        t.Fatalf("bins %d, want 32", len(spectrum.Bins))
    }
    if len(report.Breaks) != 0 {
        t.Fatalf("continuous series reported breaks: %+v", report.Breaks)
    }
    if len(report.Segments) != 1 || report.Segments[0].Frames != 64 {
        t.Fatalf("segments %+v", report.Segments)
    }
}

func TestAnalyzeSpectrumSplitsOnGap(t *testing.T) {
    start := spectrumBase
    first := sineSeries("probe-a", start, 50*time.Millisecond, 40, 0.1, 0.8)
    // Two frames are missing: the series resumes three intervals later.
    second := sineSeries("probe-a", start.Add(42*50*time.Millisecond), 50*time.Millisecond, 40, 0.1, 0.8)
    report := AnalyzeSpectrum(append(first, second...))
    if len(report.Segments) != 2 {
        t.Fatalf("segments %+v", report.Segments)
    }
    if len(report.Breaks) != 1 {
        t.Fatalf("breaks %+v", report.Breaks)
    }
    gap := report.Breaks[0]
    if gap.Kind != BreakGap {
        t.Fatalf("break kind %q, want %q", gap.Kind, BreakGap)
    }
    if !gap.At.Equal(start.Add(42 * 50 * time.Millisecond)) {
        t.Fatalf("break at %v, want resume time", gap.At)
    }
    if math.Abs(gap.BeforeHz-20) > 0.1 || math.Abs(gap.AfterHz-20) > 0.1 {
        t.Fatalf("gap rates %v -> %v, want 20 -> 20", gap.BeforeHz, gap.AfterHz)
    }
    spectrum := onlySpectrum(t, report)
    if spectrum.Status != SpectrumOK || spectrum.Frames != 40 {
        t.Fatalf("spectrum %+v, want ok over one 40-frame segment", spectrum)
    }
}

func TestAnalyzeSpectrumDetectsRateChange(t *testing.T) {
    start := spectrumBase
    first := sineSeries("probe-a", start, 50*time.Millisecond, 32, 0.1, 0.8)
    second := sineSeries("probe-a", start.Add(31*50*time.Millisecond+100*time.Millisecond), 100*time.Millisecond, 32, 0.1, 0.8)
    report := AnalyzeSpectrum(append(first, second...))
    if len(report.Segments) != 2 {
        t.Fatalf("segments %+v", report.Segments)
    }
    if len(report.Breaks) != 1 || report.Breaks[0].Kind != BreakRateChange {
        t.Fatalf("breaks %+v, want one rate_change", report.Breaks)
    }
    change := report.Breaks[0]
    if math.Abs(change.BeforeHz-20) > 0.1 || math.Abs(change.AfterHz-10) > 0.1 {
        t.Fatalf("rates %v -> %v, want 20 -> 10", change.BeforeHz, change.AfterHz)
    }
    spectrum := onlySpectrum(t, report)
    if spectrum.Status != SpectrumOK || spectrum.Frames != 32 || math.Abs(spectrum.SampleHz-20) > 0.1 {
        t.Fatalf("spectrum %+v, want ok over the 20 Hz segment", spectrum)
    }
}

func TestAnalyzeSpectrumInsufficientFrames(t *testing.T) {
    samples := sineSeries("probe-a", spectrumBase, 50*time.Millisecond, 5, 0.1, 0.8)
    spectrum := onlySpectrum(t, AnalyzeSpectrum(samples))
    if spectrum.Status != SpectrumInsufficient {
        t.Fatalf("status %q, want insufficient", spectrum.Status)
    }
    if !strings.Contains(spectrum.Reason, "5") || !strings.Contains(spectrum.Reason, "16") {
        t.Fatalf("reason %q should name the frame counts", spectrum.Reason)
    }
    if len(spectrum.Bins) != 0 || spectrum.PeakHz != 0 {
        t.Fatalf("insufficient spectrum carries bins/peak: %+v", spectrum)
    }
}

func TestAnalyzeSpectrumRejectsFlatSignal(t *testing.T) {
    samples := sineSeries("probe-a", spectrumBase, 50*time.Millisecond, 32, 0, 0)
    for index := range samples {
        samples[index].Value = 0.5
    }
    spectrum := onlySpectrum(t, AnalyzeSpectrum(samples))
    if spectrum.Status != SpectrumInsufficient || !strings.Contains(spectrum.Reason, "oscillation") {
        t.Fatalf("flat signal: %+v", spectrum)
    }
    if len(spectrum.Bins) != 0 {
        t.Fatalf("flat signal produced bins: %+v", spectrum.Bins)
    }
}

func TestAnalyzeSpectrumSeparatesSources(t *testing.T) {
    start := spectrumBase
    slow := sineSeries("probe-a", start, 50*time.Millisecond, 64, 0.1, 0.8)
    fast := sineSeries("probe-b", start, 50*time.Millisecond, 64, 0.1, 1.6)
    report := AnalyzeSpectrum(append(slow, fast...))
    if len(report.Spectra) != 2 {
        t.Fatalf("spectra %+v", report.Spectra)
    }
    peaks := map[string]Spectrum{}
    for _, spectrum := range report.Spectra {
        if spectrum.Status != SpectrumOK {
            t.Fatalf("spectrum %+v", spectrum)
        }
        peaks[spectrum.Source] = spectrum
    }
    if math.Abs(peaks["probe-a"].PeakHz-0.8) > peaks["probe-a"].ResolutionHz {
        t.Fatalf("probe-a peak %v", peaks["probe-a"].PeakHz)
    }
    if math.Abs(peaks["probe-b"].PeakHz-1.6) > peaks["probe-b"].ResolutionHz {
        t.Fatalf("probe-b peak %v", peaks["probe-b"].PeakHz)
    }
}

func TestAnalyzeSpectrumIgnoresDCOffset(t *testing.T) {
    samples := sineSeries("probe-a", spectrumBase, 50*time.Millisecond, 64, 0.1, 1.0)
    for index := range samples {
        samples[index].Value += 0.5
    }
    spectrum := onlySpectrum(t, AnalyzeSpectrum(samples))
    if spectrum.Status != SpectrumOK {
        t.Fatalf("status %q reason %q", spectrum.Status, spectrum.Reason)
    }
    if math.Abs(spectrum.PeakHz-1.0) > spectrum.ResolutionHz {
        t.Fatalf("peak %v, want 1.0 Hz rather than DC", spectrum.PeakHz)
    }
    if spectrum.PeakAmplitude < 0.06 || spectrum.PeakAmplitude > 0.14 {
        t.Fatalf("peak amplitude %v far from 0.1", spectrum.PeakAmplitude)
    }
}

func TestAnalyzeSpectrumMarksDuplicateTimestampsIrregular(t *testing.T) {
    samples := sineSeries("probe-a", spectrumBase, 50*time.Millisecond, 20, 0.1, 0.8)
    duplicate := model.NewEvidence("run-1", "rev-1", "probe-a", model.EvidenceWaveTime, 0.1, spectrumBase.Add(10*50*time.Millisecond))
    report := AnalyzeSpectrum(append(samples, duplicate))
    if len(report.Breaks) != 1 || report.Breaks[0].Kind != BreakIrregular {
        t.Fatalf("breaks %+v, want one irregular", report.Breaks)
    }
}

func TestDeriveSpectrumEmitsPeakFrequencyOnlyWhenSupported(t *testing.T) {
    samples := sineSeries("probe-a", spectrumBase, 50*time.Millisecond, 64, 0.12, 0.8)
    derived, ok := DeriveSpectrum(samples)
    if !ok {
        t.Fatal("continuous series did not yield a spectrum")
    }
    if derived.Kind != model.EvidenceWaveSpectrum {
        t.Fatalf("kind %q, want wave_spectrum", derived.Kind)
    }
    report := AnalyzeSpectrum(samples)
    if derived.Value != report.Spectra[0].PeakHz {
        t.Fatalf("derived value %v, want peak %v", derived.Value, report.Spectra[0].PeakHz)
    }
    if !derived.SamePhysicalSource(samples[0]) {
        t.Fatalf("derived evidence lost its source: %+v", derived)
    }
    if _, ok := DeriveSpectrum(samples[:3]); ok {
        t.Fatal("three frames yielded a spectrum")
    }
}
