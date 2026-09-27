package probe

import (
	"encoding/json"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func sineEvidence(source string, t0 time.Time, sampleRateHz float64, count int, frequencyHz, amplitude, phase float64) []model.Evidence {
	step := time.Duration(float64(time.Second) / sampleRateHz)
	samples := make([]model.Evidence, count)
	for k := 0; k < count; k++ {
		value := amplitude * math.Sin(2*math.Pi*frequencyHz*float64(k)/sampleRateHz+phase)
		samples[k] = model.NewEvidence("run-1", "rev-1", source, model.EvidenceWaveTime, value, t0.Add(time.Duration(k)*step))
	}
	return samples
}

func jitteredSineEvidence(source string, t0 time.Time, count int, frequencyHz float64, step time.Duration, jitterAt map[int]time.Duration) []model.Evidence {
	samples := make([]model.Evidence, count)
	at := t0
	for k := 0; k < count; k++ {
		value := math.Sin(2 * math.Pi * frequencyHz * at.Sub(t0).Seconds())
		samples[k] = model.NewEvidence("run-1", "rev-1", source, model.EvidenceWaveTime, value, at)
		if k+1 < count {
			nextStep := step
			if extra, ok := jitterAt[k]; ok {
				nextStep += extra
			}
			at = at.Add(nextStep)
		}
	}
	return samples
}

func onlySegment(t *testing.T, result []SourceSpectrum) SpectrumSegment {
	t.Helper()
	if len(result) != 1 {
		t.Fatalf("expected one source, got %d", len(result))
	}
	if len(result[0].Segments) != 1 {
		t.Fatalf("expected one segment, got %d", result[0].SegmentCount)
	}
	return result[0].Segments[0]
}

func TestSpectrumFindsSinePeakFrequencyAndScale(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	samples := sineEvidence("probe-a", t0, 50, 128, 1.0, 0.1, 0.3)
	for k := range samples {
		samples[k].Value += 0.05 // static offset must be removed as DC
	}
	result := AnalyzeSpectra(samples)
	segment := onlySegment(t, result)
	if segment.Status != SegmentOK {
		t.Fatalf("status = %s (%s)", segment.Status, segment.Reason)
	}
	peak := segment.Peak
	if peak == nil {
		t.Fatal("missing peak")
	}
	if math.Abs(peak.FrequencyHz-1.0) > 0.05 {
		t.Fatalf("peak frequency = %.4f, want ~1.0", peak.FrequencyHz)
	}
	if math.Abs(peak.SampleRateHz-50) > 0.01 || math.Abs(peak.NyquistHz-25) > 0.01 {
		t.Fatalf("rate = %.3f nyquist = %.3f", peak.SampleRateHz, peak.NyquistHz)
	}
	if peak.Points != 128 || peak.Resampled {
		t.Fatalf("points = %d resampled = %v", peak.Points, peak.Resampled)
	}
	if peak.Amplitude < 0.08 || peak.Amplitude > 0.11 {
		t.Fatalf("single-sided amplitude = %.4f, want ~0.1", peak.Amplitude)
	}
	if len(segment.FrequencyHz) != 65 || len(segment.Power) != 65 {
		t.Fatalf("spectrum bins = %d/%d, want 65", len(segment.FrequencyHz), len(segment.Power))
	}
	if segment.PeakBin < 2 || segment.PeakBin > 63 {
		t.Fatalf("peak bin = %d outside search range", segment.PeakBin)
	}
	if !segment.WindowStart.Equal(t0) {
		t.Fatalf("window start = %s", segment.WindowStart)
	}
}

func TestSpectrumIntegerBinPeakIsExact(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	frequency := 50.0 / 64.0 * 2 // 1.5625 Hz, exactly FFT bin 2 at 64 points / 50 Hz
	samples := sineEvidence("probe-a", t0, 50, 64, frequency, 0.1, 0)
	segment := onlySegment(t, AnalyzeSpectra(samples))
	if segment.Status != SegmentOK {
		t.Fatalf("status = %s (%s)", segment.Status, segment.Reason)
	}
	if math.Abs(segment.Peak.FrequencyHz-frequency) > 0.01 {
		t.Fatalf("peak frequency = %.5f, want %.5f", segment.Peak.FrequencyHz, frequency)
	}
	if math.Abs(segment.Peak.RefinedOffset) > 0.05 {
		t.Fatalf("refined offset = %.4f, want ~0", segment.Peak.RefinedOffset)
	}
	if math.Abs(segment.Peak.Amplitude-0.1) > 0.005 {
		t.Fatalf("amplitude = %.4f, want 0.1", segment.Peak.Amplitude)
	}
}

func TestSpectrumSplitsAtAbsoluteGap(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	first := sineEvidence("probe-a", t0, 50, 64, 2.0, 0.1, 0)
	second := sineEvidence("probe-a", t0.Add(5*time.Second), 50, 64, 2.0, 0.1, 0)
	result := AnalyzeSpectra(append(first, second...))
	if len(result) != 1 || result[0].SegmentCount != 2 {
		t.Fatalf("segments = %+v", result)
	}
	segments := result[0].Segments
	for index, segment := range segments {
		if segment.Status != SegmentOK {
			t.Fatalf("segment %d status = %s (%s)", index, segment.Status, segment.Reason)
		}
		if math.Abs(segment.Peak.FrequencyHz-2.0) > 0.1 {
			t.Fatalf("segment %d peak = %.3f", index, segment.Peak.FrequencyHz)
		}
	}
	if !segments[0].WindowEnd.Before(segments[1].WindowStart) {
		t.Fatal("segment windows overlap across the gap")
	}
}

func TestSpectrumSplitsAtRelativeGap(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	first := sineEvidence("probe-a", t0, 1000.0/30.0, 30, 2.0, 0.1, 0)
	second := sineEvidence("probe-a", t0.Add(30*30*time.Millisecond+300*time.Millisecond), 1000.0/30.0, 30, 2.0, 0.1, 0)
	result := AnalyzeSpectra(append(first, second...))
	if len(result) != 1 || result[0].SegmentCount != 2 {
		t.Fatalf("expected two segments split by the 300ms relative gap, got %+v", result)
	}
}

func TestSpectrumSplitsAtSampleRateStep(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	first := sineEvidence("probe-a", t0, 50, 64, 4.0, 0.1, 0)
	second := jitteredSineEvidence("probe-a", t0.Add(63*20*time.Millisecond+50*time.Millisecond), 64, 4.0, 50*time.Millisecond, nil)
	result := AnalyzeSpectra(append(first, second...))
	if len(result) != 1 || result[0].SegmentCount != 2 {
		t.Fatalf("expected two segments split by the sample-rate step, got %+v", result)
	}
	rates := []float64{result[0].Segments[0].Peak.SampleRateHz, result[0].Segments[1].Peak.SampleRateHz}
	if math.Abs(rates[0]-50) > 1 || math.Abs(rates[1]-20) > 1 {
		t.Fatalf("sample rates = %v, want ~50 and ~20", rates)
	}
}

func TestSpectrumPervasiveJitterTriggersResampleWithoutSplitting(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	// Every interval jitters independently by up to +-50%: the interval CV
	// exceeds 0.25, but no single gap is 3x the median and no four-interval
	// window sustains a different cadence, so the stream must stay one segment
	// and be recovered by linear resampling.
	rng := rand.New(rand.NewSource(7))
	const count = 100
	samples := make([]model.Evidence, count)
	at := t0
	for k := 0; k < count; k++ {
		value := 0.1 * math.Sin(2*math.Pi*2.0*at.Sub(t0).Seconds())
		samples[k] = model.NewEvidence("run-1", "rev-1", "probe-a", model.EvidenceWaveTime, value, at)
		step := time.Duration(float64(20*time.Millisecond) * (1 + (rng.Float64() - 0.5)))
		at = at.Add(step)
	}
	result := AnalyzeSpectra(samples)
	segment := onlySegment(t, result)
	if segment.Status != SegmentOK {
		t.Fatalf("status = %s (%s)", segment.Status, segment.Reason)
	}
	if !segment.Resampled {
		t.Fatal("expected the irregular segment to be resampled")
	}
	if math.Abs(segment.Peak.FrequencyHz-2.0) > 0.15 {
		t.Fatalf("resampled peak = %.3f, want ~2.0", segment.Peak.FrequencyHz)
	}
}

func TestSpectrumShortRateBurstDoesNotCreateUndersizedFakeSpectrum(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	// A burst of three slower intervals is at the edge of rate-step
	// detection. Whatever segmentation results, no segment smaller than the
	// minimum frame count may ever carry a peak, and every reported peak must
	// stay close to the true 2 Hz oscillation.
	jitterAt := map[int]time.Duration{40: 35 * time.Millisecond, 41: 35 * time.Millisecond, 42: 35 * time.Millisecond}
	samples := jitteredSineEvidence("probe-a", t0, 100, 2.0, 20*time.Millisecond, jitterAt)
	result := AnalyzeSpectra(samples)
	if len(result) != 1 {
		t.Fatalf("sources = %d", len(result))
	}
	if len(result[0].Segments) == 0 {
		t.Fatal("no segments produced")
	}
	for index, segment := range result[0].Segments {
		if segment.Status == SegmentInsufficient {
			if segment.Peak != nil {
				t.Fatalf("insufficient segment %d carries a peak", index)
			}
			continue
		}
		if segment.FrameCount < 16 {
			t.Fatalf("segment %d has %d frames but carries a peak", index, segment.FrameCount)
		}
		if math.Abs(segment.Peak.FrequencyHz-2.0) > 0.3 {
			t.Fatalf("segment %d peak = %.3f", index, segment.Peak.FrequencyHz)
		}
	}
}

func TestSpectrumRejectsInsufficientEvidenceWithoutSpectralArrays(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		samples []model.Evidence
		reason  InsufficientReason
	}{
		{"too few frames", sineEvidence("probe-a", t0, 50, 10, 1.0, 0.1, 0), ReasonTooFewFrames},
		{"flat signal", func() []model.Evidence {
			samples := sineEvidence("probe-a", t0, 50, 64, 1.0, 0.0, 0)
			for k := range samples {
				samples[k].Value = 0.24
			}
			return samples
		}(), ReasonFlatSignal},
		{"white noise", func() []model.Evidence {
			rng := rand.New(rand.NewSource(1))
			samples := make([]model.Evidence, 128)
			for k := range samples {
				samples[k] = model.NewEvidence("run-1", "rev-1", "probe-a", model.EvidenceWaveTime,
					rng.Float64()-0.5, t0.Add(time.Duration(k)*20*time.Millisecond))
			}
			return samples
		}(), ReasonPeakBelowNoiseFloor},
		{"duplicate timestamps", func() []model.Evidence {
			samples := sineEvidence("probe-a", t0, 50, 20, 1.0, 0.1, 0)
			samples[5].RecordedAt = samples[4].RecordedAt
			return samples
		}(), ReasonIrregularSampling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			segment := onlySegment(t, AnalyzeSpectra(tc.samples))
			if segment.Status != SegmentInsufficient || segment.Reason != tc.reason {
				t.Fatalf("status = %s reason = %s, want insufficient/%s", segment.Status, segment.Reason, tc.reason)
			}
			data, err := json.Marshal(segment)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"peak", "power", "frequency_hz"} {
				if _, exists := decoded[key]; exists {
					t.Fatalf("insufficient segment exposes %q: %s", key, data)
				}
			}
		})
	}
}

func TestSpectrumGroupsSourcesIndependentlyAndSorts(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	samples := sineEvidence("probe-b", t0, 50, 128, 1.0, 0.1, 0)
	samples = append(samples, sineEvidence("probe-a", t0, 50, 12, 1.0, 0.1, 0)...)
	result := AnalyzeSpectra(samples)
	if len(result) != 2 {
		t.Fatalf("sources = %d", len(result))
	}
	if result[0].SourceID != "probe-a" || result[1].SourceID != "probe-b" {
		t.Fatalf("source order = %s, %s", result[0].SourceID, result[1].SourceID)
	}
	if result[0].Status != SegmentInsufficient || result[0].Reason != ReasonTooFewFrames {
		t.Fatalf("probe-a = %s (%s)", result[0].Status, result[0].Reason)
	}
	if result[1].Status != SegmentOK || math.Abs(result[1].Segments[0].Peak.FrequencyHz-1.0) > 0.05 {
		t.Fatalf("probe-b result = %+v", result[1])
	}
}

func TestSpectrumIsDeterministicAndHandlesEmptyInput(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	samples := sineEvidence("probe-a", t0, 50, 128, 1.0, 0.1, 0)
	first := AnalyzeSpectra(samples)
	second := AnalyzeSpectra(samples)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("spectrum analysis is not deterministic")
	}
	if result := AnalyzeSpectra(nil); result != nil {
		t.Fatalf("nil input = %v, want nil", result)
	}
}
