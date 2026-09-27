package probe

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

// SegmentStatus describes whether a continuous segment supports a spectrum.
type SegmentStatus string

const (
	SegmentOK           SegmentStatus = "ok"
	SegmentInsufficient SegmentStatus = "insufficient"
)

// InsufficientReason explains why a segment produced no spectrum. A segment
// that carries one of these reasons never exposes frequency or power arrays.
type InsufficientReason string

const (
	ReasonTooFewFrames        InsufficientReason = "too_few_frames"
	ReasonIrregularSampling   InsufficientReason = "irregular_sampling"
	ReasonResampleTooShort    InsufficientReason = "resample_insufficient"
	ReasonFlatSignal          InsufficientReason = "flat_signal"
	ReasonPeakBelowNoiseFloor InsufficientReason = "peak_below_noise_floor"
	ReasonFrequencyUnresolved InsufficientReason = "frequency_unresolved"
)

// SpectrumConfig holds the thresholds for segmentation and peak detection.
// The zero value is not usable; obtain one from DefaultSpectrumConfig.
type SpectrumConfig struct {
	MinimumFrames      int
	MaxGap             time.Duration
	GapIntervalFactor  float64
	RateStepRatio      float64
	RateStepWindow     int
	MaxIntervalCV      float64
	PeakNoiseFactor    float64
	ZeroPadFactor      int
	ReturnFullSpectrum bool
}

func DefaultSpectrumConfig() SpectrumConfig {
	return SpectrumConfig{
		MinimumFrames:      16,
		MaxGap:             1500 * time.Millisecond,
		GapIntervalFactor:  3.0,
		RateStepRatio:      2.0,
		RateStepWindow:     4,
		MaxIntervalCV:      0.25,
		PeakNoiseFactor:    3.0,
		ZeroPadFactor:      1,
		ReturnFullSpectrum: true,
	}
}

// SpectrumPeak is the dominant frequency of one continuous segment.
type SpectrumPeak struct {
	FrequencyHz   float64   `json:"frequency_hz"`
	Amplitude     float64   `json:"amplitude"`
	Power         float64   `json:"power"`
	Bin           int       `json:"bin"`
	RefinedOffset float64   `json:"refined_offset"`
	SampleRateHz  float64   `json:"sample_rate_hz"`
	NyquistHz     float64   `json:"nyquist_hz"`
	ResolutionHz  float64   `json:"resolution_hz"`
	Points        int       `json:"points"`
	Resampled     bool      `json:"resampled"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
}

// SpectrumSegment is the result for one continuous time window. Insufficient
// segments only carry the status, reason and bookkeeping fields.
type SpectrumSegment struct {
	Status       SegmentStatus      `json:"status"`
	Reason       InsufficientReason `json:"reason,omitempty"`
	Detail       string             `json:"detail,omitempty"`
	WindowStart  time.Time          `json:"window_start"`
	WindowEnd    time.Time          `json:"window_end"`
	FrameCount   int                `json:"frame_count"`
	SampleRateHz float64            `json:"sample_rate_hz,omitempty"`
	Resampled    bool               `json:"resampled,omitempty"`
	Peak         *SpectrumPeak      `json:"peak,omitempty"`
	FrequencyHz  []float64          `json:"frequency_hz,omitempty"`
	Power        []float64          `json:"power,omitempty"`
	PeakBin      int                `json:"peak_bin,omitempty"`
}

// SourceSpectrum groups the per-segment results of one physical probe.
type SourceSpectrum struct {
	SourceID     string             `json:"source_id"`
	Status       SegmentStatus      `json:"status"`
	Reason       InsufficientReason `json:"reason,omitempty"`
	SegmentCount int                `json:"segment_count"`
	Segments     []SpectrumSegment  `json:"segments"`
}

// AnalyzeSpectra derives frequency spectra from continuous wave-height frames
// using the default configuration. Samples are expected to be wave_time
// evidence already filtered by the caller.
func AnalyzeSpectra(samples []model.Evidence) []SourceSpectrum {
	return AnalyzeSpectraWith(samples, DefaultSpectrumConfig())
}

// AnalyzeSpectraWith derives frequency spectra with an explicit configuration.
func AnalyzeSpectraWith(samples []model.Evidence, cfg SpectrumConfig) []SourceSpectrum {
	if len(samples) == 0 {
		return nil
	}
	groups := SameSourceGroups(samples)
	sourceIDs := make([]string, 0, len(groups))
	for sourceID := range groups {
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.Strings(sourceIDs)
	results := make([]SourceSpectrum, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		frames := append([]model.Evidence{}, groups[sourceID]...)
		sort.SliceStable(frames, func(i, j int) bool {
			return frames[i].RecordedAt.Before(frames[j].RecordedAt)
		})
		segments := splitSegments(frames, cfg)
		out := SourceSpectrum{SourceID: sourceID, SegmentCount: len(segments), Segments: make([]SpectrumSegment, 0, len(segments))}
		for _, segment := range segments {
			out.Segments = append(out.Segments, segmentSpectrum(segment, cfg))
		}
		for _, segment := range out.Segments {
			if segment.Status == SegmentOK {
				out.Status = SegmentOK
				break
			}
		}
		if out.Status == "" {
			out.Status = SegmentInsufficient
			if len(out.Segments) == 1 {
				out.Reason = out.Segments[0].Reason
			}
		}
		results = append(results, out)
	}
	return results
}

func insufficientSegment(frames []model.Evidence, reason InsufficientReason, detail string) SpectrumSegment {
	segment := SpectrumSegment{
		Status:     SegmentInsufficient,
		Reason:     reason,
		Detail:     detail,
		FrameCount: len(frames),
	}
	if len(frames) > 0 {
		segment.WindowStart = frames[0].RecordedAt
		segment.WindowEnd = frames[len(frames)-1].RecordedAt
	}
	return segment
}

// splitSegments cuts ordered frames at missing gaps and sample-rate steps.
func splitSegments(frames []model.Evidence, cfg SpectrumConfig) [][]model.Evidence {
	n := len(frames)
	if n == 0 {
		return nil
	}
	deltas := make([]time.Duration, n-1)
	for i := 0; i < n-1; i++ {
		deltas[i] = frames[i+1].RecordedAt.Sub(frames[i].RecordedAt)
	}
	segments := make([][]model.Evidence, 0, 1)
	start := 0
	local := make([]time.Duration, 0)
	for i := 0; i < n-1; i++ {
		if isSegmentBreak(frames, deltas, local, i, cfg) {
			segments = append(segments, frames[start:i+1])
			start = i + 1
			local = local[:0]
		} else {
			local = append(local, deltas[i])
		}
	}
	segments = append(segments, frames[start:n])
	return segments
}

// isSegmentBreak decides whether the boundary after frame i splits the stream.
// local holds the positive intervals already accepted into the current segment.
func isSegmentBreak(frames []model.Evidence, deltas []time.Duration, local []time.Duration, i int, cfg SpectrumConfig) bool {
	gap := deltas[i]
	if gap > cfg.MaxGap {
		return true
	}
	if gap <= 0 {
		// Duplicate or reversed timestamps stay inside the segment so that the
		// regularity check rejects it instead of hiding it as a gap.
		return false
	}
	if len(local) >= 2 {
		recent := trailingIntervals(local, cfg.RateStepWindow*8)
		medianGap := time.Duration(medianDuration(recent))
		if medianGap > 0 && float64(gap) > cfg.GapIntervalFactor*float64(medianGap) {
			return true
		}
	}
	return isRateStep(deltas, local, i, cfg)
}

func trailingIntervals(local []time.Duration, limit int) []time.Duration {
	if len(local) > limit {
		return local[len(local)-limit:]
	}
	return local
}

// isRateStep confirms a sustained sample-rate change: a single anomalous
// interval never splits, at least two following intervals must land on the
// new cadence.
func isRateStep(deltas []time.Duration, local []time.Duration, i int, cfg SpectrumConfig) bool {
	w := cfg.RateStepWindow
	if len(local) < w || i+2 >= len(deltas) {
		return false
	}
	// The baseline uses a long trailing window so that a short burst of
	// anomalous intervals ending right before i does not masquerade as a
	// sample-rate step when the cadence returns to normal.
	left := trailingIntervals(local, w*8)
	leftMedian := time.Duration(medianDuration(left))
	if leftMedian <= 0 {
		return false
	}
	// The boundary interval itself must already run at the new cadence; this
	// keeps the split at the first changed interval instead of anticipating
	// it from intervals further ahead.
	candidate := deltas[i]
	if candidate <= 0 {
		return false
	}
	slowThreshold := float64(leftMedian) * cfg.RateStepRatio
	fastThreshold := float64(leftMedian) / cfg.RateStepRatio
	slowing := float64(candidate) > slowThreshold
	speeding := float64(candidate) < fastThreshold
	if !slowing && !speeding {
		return false
	}
	rightEnd := i + 1 + w
	if rightEnd > len(deltas) {
		rightEnd = len(deltas)
	}
	confirmations := 0
	for _, delta := range deltas[i+1 : rightEnd] {
		// A missing gap inside the confirmation window describes a different
		// run; its own boundary performs the split, never this rate step.
		if delta > cfg.MaxGap || (delta > 0 && float64(delta) > cfg.GapIntervalFactor*float64(leftMedian)) {
			return false
		}
		if slowing && delta > 0 && float64(delta) > slowThreshold {
			confirmations++
		}
		if speeding && delta > 0 && float64(delta) < fastThreshold {
			confirmations++
		}
	}
	return confirmations >= 2
}

func segmentSpectrum(frames []model.Evidence, cfg SpectrumConfig) SpectrumSegment {
	n := len(frames)
	if n < cfg.MinimumFrames {
		return insufficientSegment(frames, ReasonTooFewFrames,
			fmt.Sprintf("segment has %d frames, need at least %d", n, cfg.MinimumFrames))
	}
	deltas := make([]time.Duration, n-1)
	for i := 0; i < n-1; i++ {
		deltas[i] = frames[i+1].RecordedAt.Sub(frames[i].RecordedAt)
		if deltas[i] <= 0 {
			return insufficientSegment(frames, ReasonIrregularSampling,
				"duplicate or non-monotonic sample timestamps cannot be resampled")
		}
	}
	_, _, _, cv := intervalStats(deltas)
	grid := medianDuration(deltas)
	values := make([]float64, n)
	for i, frame := range frames {
		values[i] = frame.Value
	}
	sampleRate := 1.0 / grid.Seconds()
	resampled := false
	if cv > cfg.MaxIntervalCV {
		gridValues, ok := resampleLinear(frames, grid)
		if !ok {
			return insufficientSegment(frames, ReasonIrregularSampling,
				"sample timestamps cannot be aligned to a uniform grid")
		}
		if len(gridValues) < cfg.MinimumFrames {
			return insufficientSegment(frames, ReasonResampleTooShort,
				fmt.Sprintf("uniform grid holds %d samples, need at least %d", len(gridValues), cfg.MinimumFrames))
		}
		values = gridValues
		resampled = true
	}

	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	x := make([]float64, len(values))
	peakMagnitude := 0.0
	for i, value := range values {
		centered := value - mean
		x[i] = centered
		if magnitude := math.Abs(centered); magnitude > peakMagnitude {
			peakMagnitude = magnitude
		}
	}
	if peakMagnitude <= 1e-12*math.Max(math.Abs(mean), 1) {
		return insufficientSegment(frames, ReasonFlatSignal,
			"wave height is constant, there is no oscillatory content")
	}

	points := len(x)
	applyHann(x)
	nfft := nextPowerOfTwo(points)
	if cfg.ZeroPadFactor >= 2 {
		nfft *= cfg.ZeroPadFactor
	}
	re := make([]float64, nfft)
	im := make([]float64, nfft)
	copy(re, x)
	fftRadix2(re, im)

	windowGain := hannGain(points)
	half := nfft / 2
	amplitude := make([]float64, half+1)
	power := make([]float64, half+1)
	frequency := make([]float64, half+1)
	scale := float64(points) * windowGain
	for k := 0; k <= half; k++ {
		magnitude := math.Hypot(re[k], im[k])
		if k == 0 || k == half {
			amplitude[k] = magnitude / scale
		} else {
			amplitude[k] = 2 * magnitude / scale
		}
		power[k] = amplitude[k] * amplitude[k]
		frequency[k] = float64(k) * sampleRate / float64(nfft)
	}

	bin := 1
	for k := 2; k < half; k++ {
		if power[k] > power[bin] {
			bin = k
		}
	}
	// A periodogram's extreme bin grows with the bin count even for pure
	// noise, so a fixed ratio cannot bound false peaks. log2(20M) keeps the
	// pure-noise false-alarm probability around 5% per segment; the configured
	// factor stays the floor when only a few bins are available.
	floor := medianFloat(power[1:half])
	significance := cfg.PeakNoiseFactor
	if adaptive := math.Log2(20 * float64(half-1)); adaptive > significance {
		significance = adaptive
	}
	if floor <= 0 || power[bin] < significance*floor {
		return insufficientSegment(frames, ReasonPeakBelowNoiseFloor,
			"no spectral peak stands out above the noise floor")
	}
	if bin < 2 {
		return insufficientSegment(frames, ReasonFrequencyUnresolved,
			"the dominant period is longer than this window can resolve")
	}

	offset := parabolicOffset(power[bin-1], power[bin], power[bin+1])
	peak := &SpectrumPeak{
		FrequencyHz:   (float64(bin) + offset) * sampleRate / float64(nfft),
		Amplitude:     amplitude[bin],
		Power:         power[bin],
		Bin:           bin,
		RefinedOffset: offset,
		SampleRateHz:  sampleRate,
		NyquistHz:     sampleRate / 2,
		ResolutionHz:  sampleRate / float64(nfft),
		Points:        points,
		Resampled:     resampled,
		WindowStart:   frames[0].RecordedAt,
		WindowEnd:     frames[n-1].RecordedAt,
	}
	segment := SpectrumSegment{
		Status:       SegmentOK,
		WindowStart:  frames[0].RecordedAt,
		WindowEnd:    frames[n-1].RecordedAt,
		FrameCount:   n,
		SampleRateHz: sampleRate,
		Resampled:    resampled,
		Peak:         peak,
		PeakBin:      bin,
	}
	if cfg.ReturnFullSpectrum {
		segment.FrequencyHz = frequency
		segment.Power = power
	}
	return segment
}

func intervalStats(deltas []time.Duration) (median, mean, std, cv float64) {
	if len(deltas) == 0 {
		return 0, 0, 0, math.Inf(1)
	}
	seconds := make([]float64, len(deltas))
	sum := 0.0
	for i, delta := range deltas {
		seconds[i] = delta.Seconds()
		sum += seconds[i]
	}
	mean = sum / float64(len(seconds))
	for _, value := range seconds {
		diff := value - mean
		std += diff * diff
	}
	std = math.Sqrt(std / float64(len(seconds)))
	median = medianDuration(deltas).Seconds()
	if mean > 0 {
		cv = std / mean
	} else {
		cv = math.Inf(1)
	}
	return median, mean, std, cv
}

// resampleLinear places values on a uniform grid starting at the first frame,
// with no extrapolation past the last frame.
func resampleLinear(frames []model.Evidence, grid time.Duration) ([]float64, bool) {
	if grid <= 0 || len(frames) < 2 {
		return nil, false
	}
	first := frames[0].RecordedAt
	last := frames[len(frames)-1].RecordedAt
	span := last.Sub(first)
	if span <= 0 {
		return nil, false
	}
	count := int(span/grid) + 1
	out := make([]float64, 0, count)
	j := 0
	for k := 0; k < count; k++ {
		at := first.Add(time.Duration(k) * grid)
		for j+1 < len(frames) && !frames[j+1].RecordedAt.After(at) {
			j++
		}
		if j == len(frames)-1 {
			out = append(out, frames[j].Value)
			continue
		}
		tA, tB := frames[j].RecordedAt, frames[j+1].RecordedAt
		step := tB.Sub(tA)
		if step <= 0 {
			return nil, false
		}
		frac := float64(at.Sub(tA)) / float64(step)
		out = append(out, frames[j].Value+frac*(frames[j+1].Value-frames[j].Value))
	}
	return out, true
}

func applyHann(x []float64) {
	n := len(x)
	if n == 1 {
		return
	}
	denominator := float64(n - 1)
	for i := range x {
		x[i] *= 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/denominator))
	}
}

func hannGain(n int) float64 {
	if n <= 1 {
		return 1
	}
	sum := 0.0
	denominator := float64(n - 1)
	for i := 0; i < n; i++ {
		sum += 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/denominator))
	}
	return sum / float64(n)
}

// fftRadix2 performs an in-place iterative FFT; len(re) must be a power of two.
func fftRadix2(re, im []float64) {
	n := len(re)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for j&bit != 0 {
			j ^= bit
			bit >>= 1
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		wr, wi := math.Cos(angle), math.Sin(angle)
		half := length / 2
		for start := 0; start < n; start += length {
			cwr, cwi := 1.0, 0.0
			for j := 0; j < half; j++ {
				idx := start + j + half

				tr := cwr*re[idx] - cwi*im[idx]
				ti := cwr*im[idx] + cwi*re[idx]
				re[idx] = re[start+j] - tr
				im[idx] = im[start+j] - ti
				re[start+j] += tr
				im[start+j] += ti
				cwr, cwi = cwr*wr-cwi*wi, cwr*wi+cwi*wr
			}
		}
	}
}

func nextPowerOfTwo(n int) int {
	power := 1
	for power < n {
		power <<= 1
	}
	return power
}

// parabolicOffset returns the sub-bin offset of a quadratic peak through three
// power samples, clamped to [-1, 1].
func parabolicOffset(left, center, right float64) float64 {
	denominator := left - 2*center + right
	if denominator == 0 {
		return 0
	}
	offset := 0.5 * (left - right) / denominator
	if offset > 1 {
		return 1
	}
	if offset < -1 {
		return -1
	}
	return offset
}

func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration{}, values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
