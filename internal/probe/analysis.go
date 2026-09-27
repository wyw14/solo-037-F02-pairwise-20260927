package probe

import (
    "math"
    "sort"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

type Analysis struct {
    Run model.RunID `json:"run"`
    Revision model.RevisionID `json:"revision"`
    Sources []string `json:"sources"`
    Count int `json:"count"`
    Minimum float64 `json:"minimum"`
    Maximum float64 `json:"maximum"`
    Mean float64 `json:"mean"`
    RMS float64 `json:"rms"`
    Span time.Duration `json:"span"`
    First time.Time `json:"first"`
    Last time.Time `json:"last"`
}

func Analyze(samples []model.Evidence) Analysis {
    if len(samples) == 0 {
        return Analysis{}
    }
    ordered := append([]model.Evidence{}, samples...)
    sort.SliceStable(ordered, func(i int, j int) bool {
        return ordered[i].RecordedAt.Before(ordered[j].RecordedAt)
    })
    result := Analysis{
        Run: ordered[0].Run,
        Revision: ordered[0].Revision,
        Count: len(ordered),
        Minimum: ordered[0].Value,
        Maximum: ordered[0].Value,
        First: ordered[0].RecordedAt,
        Last: ordered[len(ordered)-1].RecordedAt,
    }
    sourceSet := make(map[string]struct{})
    sum := 0.0
    squares := 0.0
    for _, sample := range ordered {
        if sample.Value < result.Minimum {
            result.Minimum = sample.Value
        }
        if sample.Value > result.Maximum {
            result.Maximum = sample.Value
        }
        sum += sample.Value
        squares += sample.Value * sample.Value
        sourceSet[sample.SourceID] = struct{}{}
    }
    result.Mean = sum / float64(result.Count)
    result.RMS = math.Sqrt(squares / float64(result.Count))
    result.Span = result.Last.Sub(result.First)
    for source := range sourceSet {
        result.Sources = append(result.Sources, source)
    }
    sort.Strings(result.Sources)
    return result
}

func FilterRevision(samples []model.Evidence, revision model.RevisionID) []model.Evidence {
    result := make([]model.Evidence, 0, len(samples))
    for _, sample := range samples {
        if sample.Revision == revision {
            result = append(result, sample)
        }
    }
    return result
}

func FilterRun(samples []model.Evidence, run model.RunID) []model.Evidence {
    result := make([]model.Evidence, 0, len(samples))
    for _, sample := range samples {
        if sample.Run == run {
            result = append(result, sample)
        }
    }
    return result
}

func SameSourceGroups(samples []model.Evidence) map[string][]model.Evidence {
    result := make(map[string][]model.Evidence)
    for _, sample := range samples {
        result[sample.SourceID] = append(result[sample.SourceID], sample)
    }
    return result
}

func Continuous(samples []model.Evidence, maximumGap time.Duration) bool {
    if len(samples) < 2 {
        return len(samples) == 1
    }
    ordered := append([]model.Evidence{}, samples...)
    sort.SliceStable(ordered, func(i int, j int) bool {
        return ordered[i].RecordedAt.Before(ordered[j].RecordedAt)
    })
    for index := 1; index < len(ordered); index++ {
        if ordered[index].RecordedAt.Sub(ordered[index-1].RecordedAt) > maximumGap {
            return false
        }
    }
    return true
}

func Durable(samples []model.Evidence) bool {
    if len(samples) == 0 {
        return false
    }
    for _, sample := range samples {
        if !sample.Durable {
            return false
        }
    }
    return true
}

func Normalize(samples []model.Evidence) []model.Evidence {
    analysis := Analyze(samples)
    if analysis.Count == 0 || analysis.Maximum == analysis.Minimum {
        return append([]model.Evidence{}, samples...)
    }
    scale := analysis.Maximum - analysis.Minimum
    result := make([]model.Evidence, 0, len(samples))
    for _, sample := range samples {
        sample.Value = (sample.Value - analysis.Minimum) / scale
        result = append(result, sample)
    }
    return result
}
