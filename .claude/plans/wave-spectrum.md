# 波高分析扩展：基于连续实测帧的频谱

## 背景（现状）

- `internal/probe/analysis.go` — `Analyze()` 给出波高 min/max/mean/RMS/span；`DeriveSpectrum(sample)` 只对**单帧取绝对值**就标记为 `wave_spectrum`，无法看出主要波频（仅被 `service/reachability.go` 诊断使用）。
- `internal/service/measurements.go` — `MeasurementArchive.Analyze()` 汇总 `Wave probe.Analysis` + force 统计，经 `GET /api/measured-runs/{id}/analysis` 输出；原始证据经 `GET /api/measured-runs/{id}` 可回看（保持不变）。
- 无频谱相关 UI（web 页只是 JSON 转储），本次只扩展 API 数据路径。

## 目标

1. 频谱基于**连续实测帧**（同一 source 的 `wave_time` 序列）计算，替代单帧取绝对值。
2. 分段时**区分采样率变化和缺口**，并在结果中报告。
3. 给出**峰值频率及对应时间窗**（每个 source 各一份）。
4. 原始波高统计与原始证据保持可回看，不改动。
5. **证据不足时不画虚假的谱**：返回 `status: "insufficient"` + 原因，无 bins/peak。

## 实现

### 1. 新增 `internal/probe/spectrum.go`

类型：

```go
type BreakKind string            // "gap" | "rate_change"
type Break struct { Source string; At time.Time; Kind BreakKind; BeforeHz, AfterHz float64 }
type Segment struct { Source string; First, Last time.Time; Frames int; SampleHz float64 }
type SpectrumBin struct { Hz, Amplitude float64 }
type Spectrum struct {
    Status string                 // "ok" | "insufficient"
    Reason string                 // insufficient 时的原因
    Source string
    Frames int                    // 参与计算的帧数
    SampleHz, ResolutionHz float64
    WindowFirst, WindowLast time.Time   // 峰值频率对应的时间窗
    PeakHz, PeakAmplitude float64
    Bins []SpectrumBin            // 仅 ok 时填充，不含直流分量
}
type SpectralReport struct {
    Spectra  []Spectrum           // 每个 source 一份（取该 source 最长合格段）
    Segments []Segment            // 全部合格段
    Breaks   []Break              // 全部分段事件（缺口/采样率变化）
}
```

函数：

- `AnalyzeSpectrum(samples []model.Evidence) SpectralReport`
  - 过滤 `Kind == wave_time`，按 `SourceID` 分组（复用 `SameSourceGroups`），各组按 `RecordedAt` 排序。
  - **分段**：帧间间隔相对当前段标称间隔（段内中位数）偏离超过 `gapRatio = 1.5` 倍即断段。
  - **断点分类**：比较断点前后两段标称采样率——一致（容差 `rateTolerance = 0.25`）→ `gap`（丢帧）；不一致 → `rate_change`。
  - **充分性**：段帧数 ≥ `minSpectrumFrames = 16` 才合格；否则该 source 记 `insufficient`（原因如 `"only 5 continuous frames (need 16)"`、`"no continuous segment"`）。
  - **谱计算**（最长合格段）：去均值 → Hann 窗 → 朴素 DFT（O(N²)，操作员数据量小，零新依赖）→ 幅值归一 `2|X(k)|/Σw`（纯正弦峰值≈其振幅）。bins 取 1..N/2（排除直流）。峰值 = 最大 bin；峰值幅值 ≤ ε（信号无振荡，如恒定值）→ `insufficient`，原因 `"no measurable oscillation"`。
- `DeriveSpectrum(samples []model.Evidence) (model.Evidence, bool)` — **替换**旧单帧版本：内部走 `AnalyzeSpectrum`，最佳谱 ok 时返回 `wave_spectrum` 证据（`Value = PeakHz`，沿用该段 run/revision/source）；不足时返回 `false`，不产生证据。

### 2. `internal/probe/analysis.go`

- 删除旧 `DeriveSpectrum(sample)`（迁移至 spectrum.go 并改签名）。`Analyze`/`Continuous` 等其余函数不动（permit 策略热路径不受影响）。

### 3. `internal/service/measurements.go`

- `MeasurementAnalysis` 增加 `Spectrum probe.SpectralReport \`json:"spectrum"\``。
- `Analyze()` 用 run 内 wave 证据调用 `probe.AnalyzeSpectrum`；`Wave` 统计与原始证据端点保持不变。

### 4. `internal/service/reachability.go`

- 构造一小段连续合成序列（如 32 帧、固定 50ms 间隔的正弦）调用新 `DeriveSpectrum(series)`；`same_source`/`source_groups`/`normalized`/`revision_samples` 等诊断改用新返回值适配。

### 5. 测试

- 新增 `internal/probe/spectrum_test.go`：
  - 20 Hz 采样 0.8 Hz 正弦 64 帧 → 峰值在一个分辨率 bin 内≈0.8 Hz，时间窗=首末帧，SampleHz=20，幅值≈正弦振幅。
  - 含一个 3× 间隔的序列 → 两段 + 一个 `gap` 断点；谱取自较长段。
  - 20 Hz → 10 Hz 中途变速 → `rate_change` 断点，两段采样率各正确。
  - 仅 5 帧 → `insufficient`，原因含帧数，无 bins。
  - 恒定信号 → `insufficient`（"no measurable oscillation"）。
  - 两个 source → 两份谱，各自时间窗正确。
  - 带直流偏移的正弦 → 峰值不在 0 Hz。
- 扩展 `internal/service/measurements_test.go`：录入连续波形序列 → 分析含 ok 谱与预期峰值；原始证据条数与现有统计不变。
- 扩展 `internal/api/measurements_test.go`：合成序列的分析 JSON 含 `"status":"ok"` 与 `peak_hz`；单帧 run → `"status":"insufficient"` 且无 bins（断言不产出虚假谱）。

### 6. README.md

- 补一段：analysis 现含按 source 的频谱（时间窗/峰值频率）、缺口与采样率变化分段、证据不足时的行为。

## 验证

`go build ./... && go test ./...` 全绿。
