package probe

import (
    "fmt"
    "math"
    "sort"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

// Spectrum tuning constants. Operator-entered series are short and may
// contain missed frames or sampling-rate changes, so spectra are only
// computed over continuous, uniformly sampled segments.
const (
    // minSpectrumFrames is the smallest segment that yields a meaningful
    // amplitude spectrum.
    minSpectrumFrames = 16
    // rateTolerance bounds the relative deviation of frame intervals inside
    // one segment; larger deviations split the series.
    rateTolerance = 0.25
    // minOscillation is the peak amplitude below which a segment is reported
    // as having no measurable wave frequency.
    minOscillation = 1e-9
)

type SpectrumStatus string

const (
    SpectrumOK           SpectrumStatus = "ok"
    SpectrumInsufficient SpectrumStatus = "insufficient"
)

type BreakKind string

const (
    // BreakGap marks missed frames: the series continues at the same rate
    // after the break.
    BreakGap BreakKind = "gap"
    // BreakRateChange marks a break where the series resumes at a different
    // sampling rate.
    BreakRateChange BreakKind = "rate_change"
    // BreakIrregular marks a break that is neither a clean gap nor a rate
    // change, such as a duplicated or out-of-order timestamp.
    BreakIrregular BreakKind = "irregular"
)

// Break records where a source's series was split and why. At is the
// timestamp of the first frame after the break.
type Break struct {
    Source   string    `json:"source"`
    At       time.Time `json:"at"`
    Kind     BreakKind `json:"kind"`
    BeforeHz float64   `json:"before_hz"`
    AfterHz  float64   `json:"after_hz"`
}

// Segment describes one continuous, uniformly sampled run of frames.
type Segment struct {
    Source   string    `json:"source"`
    First    time.Time `json:"first"`
    Last     time.Time `json:"last"`
    Frames   int       `json:"frames"`
    SampleHz float64   `json:"sample_hz"`
}

type SpectrumBin struct {
    Hz        float64 `json:"hz"`
    Amplitude float64 `json:"amplitude"`
}

// Spectrum is the amplitude spectrum of one source's longest qualifying
// segment. When Status is SpectrumInsufficient no spectrum was computed and
// Reason explains why; Bins and PeakHz stay empty so an unsupported spectrum
// is never shown.
type Spectrum struct {
    Status        SpectrumStatus `json:"status"`
    Reason        string         `json:"reason,omitempty"`
    Source        string         `json:"source"`
    Frames        int            `json:"frames"`
    SampleHz      float64        `json:"sample_hz,omitempty"`
    ResolutionHz  float64        `json:"resolution_hz,omitempty"`
    WindowFirst   time.Time      `json:"window_first"`
    WindowLast    time.Time      `json:"window_last"`
    PeakHz        float64        `json:"peak_hz,omitempty"`
    PeakAmplitude float64        `json:"peak_amplitude,omitempty"`
    Bins          []SpectrumBin  `json:"bins,omitempty"`
}

// SpectralReport carries one spectrum per source plus the segmentation the
// spectra were computed from.
type SpectralReport struct {
    Spectra  []Spectrum `json:"spectra"`
    Segments []Segment  `json:"segments"`
    Breaks   []Break    `json:"breaks"`
}

type frame struct {
    at    time.Time
    value float64
}

// segmentRange is a half-open run of frames, [start, end] inclusive, with the
// median of its internal intervals in seconds.
type segmentRange struct {
    start   int
    end     int
    nominal float64
}

// AnalyzeSpectrum computes a per-source amplitude spectrum from wave_time
// evidence. Each source's series is split into continuous, uniformly sampled
// segments; breaks are classified as gaps or sampling-rate changes; the
// longest qualifying segment per source is transformed. Sources without a
// qualifying segment get an insufficient spectrum with a reason.
func AnalyzeSpectrum(samples []model.Evidence) SpectralReport {
    report := SpectralReport{Spectra: []Spectrum{}, Segments: []Segment{}, Breaks: []Break{}}
    waves := make([]model.Evidence, 0, len(samples))
    for _, sample := range samples {
        if sample.Kind == model.EvidenceWaveTime {
            waves = append(waves, sample)
        }
    }
    groups := SameSourceGroups(waves)
    sources := SourceIDs(waves)
    sort.Strings(sources)
    for _, source := range sources {
        frames := framesOf(groups[source])
        ranges, boundaries := splitSegments(frames)
        for index, boundary := range boundaries {
            left, right := ranges[index], ranges[index+1]
            report.Breaks = append(report.Breaks, Break{
                Source:   source,
                At:       frames[right.start].at,
                Kind:     classifyBreak(boundary, left, right),
                BeforeHz: rateOf(left),
                AfterHz:  rateOf(right),
            })
        }
        for _, segment := range ranges {
            report.Segments = append(report.Segments, toSegment(source, frames, segment))
        }
        report.Spectra = append(report.Spectra, sourceSpectrum(source, frames, ranges))
    }
    return report
}

// DeriveSpectrum emits a wave_spectrum evidence carrying the peak frequency
// of the best continuous segment. It returns false when the samples contain
// no qualifying segment, so a spectrum is never fabricated from thin data.
func DeriveSpectrum(samples []model.Evidence) (model.Evidence, bool) {
    report := AnalyzeSpectrum(samples)
    best := Spectrum{}
    found := false
    for _, spectrum := range report.Spectra {
        if spectrum.Status == SpectrumOK && (!found || spectrum.Frames > best.Frames) {
            best, found = spectrum, true
        }
    }
    if !found {
        return model.Evidence{}, false
    }
    for _, sample := range samples {
        if sample.SourceID == best.Source && !sample.RecordedAt.Before(best.WindowFirst) && !sample.RecordedAt.After(best.WindowLast) {
            return model.NewEvidence(sample.Run, sample.Revision, best.Source, model.EvidenceWaveSpectrum, best.PeakHz, best.WindowLast), true
        }
    }
    return model.Evidence{}, false
}

func framesOf(samples []model.Evidence) []frame {
    ordered := append([]model.Evidence{}, samples...)
    sort.SliceStable(ordered, func(i int, j int) bool {
        return ordered[i].RecordedAt.Before(ordered[j].RecordedAt)
    })
    frames := make([]frame, 0, len(ordered))
    for _, sample := range ordered {
        frames = append(frames, frame{at: sample.RecordedAt, value: sample.Value})
    }
    return frames
}

// splitSegments walks the series and breaks it wherever an interval deviates
// from the current segment's running nominal interval by more than
// rateTolerance, or is not positive. It returns the segment ranges and, for
// each boundary between consecutive segments, the interval across it.
func splitSegments(frames []frame) ([]segmentRange, []float64) {
    if len(frames) == 0 {
        return nil, nil
    }
    segments := []segmentRange{}
    boundaries := []float64{}
    start := 0
    intervals := []float64{}
    for index := 1; index < len(frames); index++ {
        dt := frames[index].at.Sub(frames[index-1].at).Seconds()
        nominal := median(intervals)
        if dt <= 0 || (len(intervals) > 0 && math.Abs(dt-nominal) > rateTolerance*nominal) {
            segments = append(segments, segmentRange{start: start, end: index - 1, nominal: nominal})
            boundaries = append(boundaries, dt)
            start = index
            intervals = nil
            continue
        }
        intervals = append(intervals, dt)
    }
    segments = append(segments, segmentRange{start: start, end: len(frames) - 1, nominal: median(intervals)})
    return segments, boundaries
}

// classifyBreak distinguishes gaps from sampling-rate changes by comparing
// the nominal rates of the segments on both sides of a break.
func classifyBreak(boundary float64, left segmentRange, right segmentRange) BreakKind {
    if boundary <= 0 {
        return BreakIrregular
    }
    if left.nominal > 0 && right.nominal > 0 {
        if math.Abs(left.nominal-right.nominal) > rateTolerance*math.Max(left.nominal, right.nominal) {
            return BreakRateChange
        }
        nominal := 0.5 * (left.nominal + right.nominal)
        if boundary > nominal*(1+rateTolerance) {
            return BreakGap
        }
        return BreakIrregular
    }
    // One side is a single frame, so only one rate is known: an oversized
    // boundary interval reads as missed frames, anything else as a new rate.
    known := left.nominal
    if known <= 0 {
        known = right.nominal
    }
    if known <= 0 {
        return BreakIrregular
    }
    if boundary > known*(1+rateTolerance) {
        return BreakGap
    }
    return BreakRateChange
}

func rateOf(segment segmentRange) float64 {
    if segment.nominal <= 0 {
        return 0
    }
    return 1 / segment.nominal
}

func toSegment(source string, frames []frame, segment segmentRange) Segment {
    first, last := frames[segment.start], frames[segment.end]
    sampleHz := 0.0
    if span := last.at.Sub(first.at).Seconds(); span > 0 {
        sampleHz = float64(segment.end-segment.start) / span
    }
    return Segment{Source: source, First: first.at, Last: last.at, Frames: segment.end - segment.start + 1, SampleHz: sampleHz}
}

// sourceSpectrum picks the source's longest qualifying segment and computes
// its spectrum. Without a qualifying segment it reports why the evidence is
// insufficient instead of deriving a spectrum from too few frames.
func sourceSpectrum(source string, frames []frame, segments []segmentRange) Spectrum {
    best := -1
    longest := 0
    for index, segment := range segments {
        count := segment.end - segment.start + 1
        if count > longest {
            longest = count
        }
        if count >= minSpectrumFrames && (best == -1 || count > segments[best].end-segments[best].start+1) {
            best = index
        }
    }
    if best == -1 {
        return Spectrum{
            Status: SpectrumInsufficient,
            Reason: fmt.Sprintf("longest continuous segment has %d frames, need at least %d", longest, minSpectrumFrames),
            Source: source,
        }
    }
    segment := segments[best]
    spectrum := computeSpectrum(source, frames[segment.start:segment.end+1])
    if spectrum.PeakAmplitude <= minOscillation {
        return Spectrum{
            Status:      SpectrumInsufficient,
            Reason:      "signal has no measurable oscillation",
            Source:      source,
            Frames:      spectrum.Frames,
            SampleHz:    spectrum.SampleHz,
            WindowFirst: spectrum.WindowFirst,
            WindowLast:  spectrum.WindowLast,
        }
    }
    return spectrum
}

// computeSpectrum applies a Hann window to the mean-removed frames and
// evaluates the DFT directly: operator series are short, so the O(n²)
// transform is cheap and exact for any frame count. Amplitudes are corrected
// for the window's coherent gain so a pure sine reads its true amplitude.
func computeSpectrum(source string, window []frame) Spectrum {
    count := len(window)
    first, last := window[0].at, window[count-1].at
    dt := last.Sub(first).Seconds() / float64(count-1)
    mean := 0.0
    for _, sample := range window {
        mean += sample.value
    }
    mean /= float64(count)
    weights := make([]float64, count)
    weightSum := 0.0
    for index := range weights {
        weights[index] = 0.5 * (1 - math.Cos(2*math.Pi*float64(index)/float64(count-1)))
        weightSum += weights[index]
    }
    bins := make([]SpectrumBin, 0, count/2)
    peakHz, peakAmplitude := 0.0, 0.0
    for k := 1; k <= count/2; k++ {
        real, imag := 0.0, 0.0
        for index, sample := range window {
            phase := 2 * math.Pi * float64(k) * float64(index) / float64(count)
            real += (sample.value - mean) * weights[index] * math.Cos(phase)
            imag -= (sample.value - mean) * weights[index] * math.Sin(phase)
        }
        amplitude := 2 * math.Hypot(real, imag) / weightSum
        if k == count/2 && count%2 == 0 {
            // The Nyquist bin has no negative-frequency counterpart.
            amplitude /= 2
        }
        hz := float64(k) / (float64(count) * dt)
        bins = append(bins, SpectrumBin{Hz: hz, Amplitude: amplitude})
        if amplitude > peakAmplitude {
            peakHz, peakAmplitude = hz, amplitude
        }
    }
    return Spectrum{
        Status:        SpectrumOK,
        Source:        source,
        Frames:        count,
        SampleHz:      1 / dt,
        ResolutionHz:  1 / (float64(count) * dt),
        WindowFirst:   first,
        WindowLast:    last,
        PeakHz:        peakHz,
        PeakAmplitude: peakAmplitude,
        Bins:          bins,
    }
}

func median(values []float64) float64 {
    if len(values) == 0 {
        return 0
    }
    ordered := append([]float64{}, values...)
    sort.Float64s(ordered)
    middle := len(ordered) / 2
    if len(ordered)%2 == 1 {
        return ordered[middle]
    }
    return 0.5 * (ordered[middle-1] + ordered[middle])
}
