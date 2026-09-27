package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/probe"
)

// MeasuredRun contains observations supplied by an operator, unlike the
// deterministic diagnostic scenario used by the legacy equipment page.
type MeasuredRun struct {
	Operation model.Operation  `json:"operation"`
	Evidence  []model.Evidence `json:"evidence"`
}

type MeasurementInput struct {
	Kind       model.EvidenceKind `json:"kind"`
	SourceID   string             `json:"source_id"`
	Value      float64            `json:"value"`
	RecordedAt time.Time          `json:"recorded_at"`
}

type MeasurementAnalysis struct {
	Operation  model.Operation        `json:"operation"`
	Wave       probe.Analysis         `json:"wave"`
	Spectra    []probe.SourceSpectrum `json:"spectra,omitempty"`
	ForceCount int                    `json:"force_count"`
	ForceMean  float64                `json:"force_mean"`
	Evaluated  time.Time              `json:"evaluated_at"`
}

type MeasurementArchive struct {
	mu   sync.Mutex
	path string
	runs map[model.RunID]MeasuredRun
}

func OpenMeasurementArchive(directory string) (*MeasurementArchive, error) {
	a := &MeasurementArchive{runs: make(map[model.RunID]MeasuredRun)}
	if directory == "" {
		return a, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	a.path = filepath.Join(directory, "operator-measurements.json")
	data, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &a.runs); err != nil {
		return nil, fmt.Errorf("read measurement archive: %w", err)
	}
	if a.runs == nil {
		return nil, errors.New("measurement archive must contain a run map")
	}
	return a, nil
}

func cloneMeasuredRun(value MeasuredRun) MeasuredRun {
	value.Evidence = append([]model.Evidence(nil), value.Evidence...)
	return value
}

func (a *MeasurementArchive) commit(next map[model.RunID]MeasuredRun) error {
	if a.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(a.path), "measurements-*.tmp")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(name, a.path); err != nil {
			return err
		}
	}
	a.runs = next
	return nil
}

func (a *MeasurementArchive) Create(name, modelName, section, carriage string) (MeasuredRun, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(modelName) == "" || strings.TrimSpace(section) == "" || strings.TrimSpace(carriage) == "" {
		return MeasuredRun{}, errors.New("name, model, section and carriage are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	operation := model.NewOperation(name, modelName, section, carriage, time.Now().UTC())
	value := MeasuredRun{Operation: operation, Evidence: []model.Evidence{}}
	next := make(map[model.RunID]MeasuredRun, len(a.runs)+1)
	for id, run := range a.runs {
		next[id] = run
	}
	next[operation.ID] = value
	if err := a.commit(next); err != nil {
		return MeasuredRun{}, err
	}
	return value, nil
}

func (a *MeasurementArchive) Record(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	if input.Kind != model.EvidenceWaveTime && input.Kind != model.EvidenceForce {
		return model.Evidence{}, errors.New("measurement kind must be wave_time or force")
	}
	if strings.TrimSpace(input.SourceID) == "" || math.IsNaN(input.Value) || math.IsInf(input.Value, 0) || input.RecordedAt.IsZero() || input.RecordedAt.After(time.Now().Add(time.Minute)) {
		return model.Evidence{}, errors.New("source, finite value and valid observation time are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return model.Evidence{}, errors.New("run not found")
	}
	evidence := model.NewEvidence(id, run.Operation.Revision, input.SourceID, input.Kind, input.Value, input.RecordedAt).WithDurability(a.path != "")
	run = cloneMeasuredRun(run)
	run.Evidence = append(run.Evidence, evidence)
	next := make(map[model.RunID]MeasuredRun, len(a.runs))
	for key, value := range a.runs {
		next[key] = value
	}
	next[id] = run
	if err := a.commit(next); err != nil {
		return model.Evidence{}, err
	}
	return evidence, nil
}

func (a *MeasurementArchive) Get(id model.RunID) (MeasuredRun, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	return cloneMeasuredRun(run), exists
}

func (a *MeasurementArchive) List() []MeasuredRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]MeasuredRun, 0, len(a.runs))
	for _, run := range a.runs {
		result = append(result, cloneMeasuredRun(run))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Operation.CreatedAt.Before(result[j].Operation.CreatedAt) })
	return result
}

func (a *MeasurementArchive) Analyze(id model.RunID) (MeasurementAnalysis, error) {
	run, exists := a.Get(id)
	if !exists {
		return MeasurementAnalysis{}, errors.New("run not found")
	}
	waves := make([]model.Evidence, 0)
	result := MeasurementAnalysis{Operation: run.Operation, Evaluated: time.Now().UTC()}
	for _, evidence := range run.Evidence {
		if evidence.Kind == model.EvidenceWaveTime {
			waves = append(waves, evidence)
		} else if evidence.Kind == model.EvidenceForce {
			result.ForceCount++
			result.ForceMean += evidence.Value
		}
	}
	result.Wave = probe.Analyze(waves)
	result.Spectra = probe.AnalyzeSpectra(waves)
	if result.ForceCount > 0 {
		result.ForceMean /= float64(result.ForceCount)
	}
	return result, nil
}

func (r *Runtime) CreateMeasuredRun(name, modelName, section, carriage string) (MeasuredRun, error) {
	return r.Measurements.Create(name, modelName, section, carriage)
}

func (r *Runtime) RecordMeasurement(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	return r.Measurements.Record(id, input)
}

func (r *Runtime) MeasuredRuns() []MeasuredRun { return r.Measurements.List() }

func (r *Runtime) MeasuredRun(id model.RunID) (MeasuredRun, bool) { return r.Measurements.Get(id) }

func (r *Runtime) AnalyzeMeasuredRun(id model.RunID) (MeasurementAnalysis, error) {
	return r.Measurements.Analyze(id)
}
