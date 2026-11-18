package poststack

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

const BandpassAlgorithmID = "seisforge.bandpass.zero_phase"
const BandpassLowParameter = "low_hz"
const BandpassHighParameter = "high_hz"
const FilterResponseParameter = "response"
const FilterMethodParameter = "method"
const FilterOrderParameter = "order"
const FilterPhaseParameter = "phase"
const FilterRippleParameter = "ripple_db"
const FilterAttenuationParameter = "attenuation_db"

// BandpassDescriptor is shared by the public module's generated form and the
// spectrum view controls. Frequencies remain drafts until explicit application.
func BandpassDescriptor(dtUS int) api.AlgorithmDescriptor {
	if dtUS <= 0 {
		dtUS = 2000 // editable preset only; execution always uses the real dt.
	}
	p := DefaultBandpass(dtUS)
	min, orderMin, orderMax := 0.0, 1.0, float64(FilterMaxOrder)
	rippleMin, rippleMax := FilterMinRippleDB, float64(FilterMaxRippleDB)
	attenuationMin, attenuationMax := float64(FilterMinAttenuationDB), float64(FilterMaxAttenuationDB)
	common := func(order int) *api.ParameterUI {
		return &api.ParameterUI{Section: api.ParameterUICommon, Order: order}
	}
	advanced := &api.ParameterUI{Section: api.ParameterUIAdvanced}
	return api.AlgorithmDescriptor{ID: BandpassAlgorithmID, Version: "1.1.0", Name: "逐道时间滤波",
		Category: "filter", Description: "沿时间轴逐道对基准 A 滤波；读取所选道完整真实时间上下文后裁回当前时间窗。支持 Butterworth、Chebyshev I/II 和 Elliptic 的 IIR 二阶节，可单向或双向执行，不混合相邻道。",
		ProtocolVersion: api.ProtocolVersion, InputKinds: []string{"section2d"}, OutputKinds: []string{"section2d"},
		SupportsProgress: true, SupportsCancel: true, Executable: "host-builtin-bandpass", MinimumHostVersion: "1.10.3",
		Parameters: []api.ParameterDescriptor{
			{ID: FilterResponseParameter, Type: "enum", Name: "响应类型", Default: string(p.EffectiveResponse()), Options: []string{string(FilterLowpass), string(FilterHighpass), string(FilterBandpass), string(FilterBandstop), string(FilterLegacyBandpass)}, UI: common(0)},
			{ID: FilterMethodParameter, Type: "enum", Name: "设计方法", Default: string(p.EffectiveMethod()), Options: []string{string(FilterButterworth), string(FilterChebyshevI), string(FilterChebyshevII), string(FilterElliptic)}, UI: common(1)},
			{ID: BandpassLowParameter, Type: "number", Name: "边界频率 / 下界", Description: "必须在 0 与当前 Nyquist 之间", Unit: "Hz", Required: true, Default: p.LowHz, Min: &min, UI: common(2)},
			{ID: BandpassHighParameter, Type: "number", Name: "边界上限", Description: "高于下界，低于当前 Nyquist", Unit: "Hz", Required: true, Default: p.HighHz, Min: &min, UI: common(3)},
			{ID: FilterOrderParameter, Type: "integer", Name: "设计阶数", Description: "带通/带阻单程为设计阶数的两倍", Default: p.DesignOrder(), Min: &orderMin, Max: &orderMax, UI: common(4)},
			{ID: FilterPhaseParameter, Type: "enum", Name: "相位方式", Description: "双向响应为单程幅值的平方", Default: string(p.EffectivePhase()), Options: []string{string(FilterForward), string(FilterZeroPhase)}, UI: common(5)},
			{ID: FilterRippleParameter, Type: "number", Name: "通带纹波 Rp", Unit: "dB", Default: 1., Min: &rippleMin, Max: &rippleMax, UI: advanced},
			{ID: FilterAttenuationParameter, Type: "number", Name: "阻带衰减 Rs", Unit: "dB", Default: 60., Min: &attenuationMin, Max: &attenuationMax, UI: advanced},
		}}
}

func BandpassParameters(values map[string]any, dtUS int) (ViewBandpass, error) {
	schema := BandpassDescriptor(dtUS).Parameters
	response, _ := values[FilterResponseParameter].(string)
	method, _ := values[FilterMethodParameter].(string)
	applicable := make(map[string]any, len(values))
	for id, value := range values {
		if FilterParameterApplies(id, FilterResponse(response), FilterMethod(method)) {
			applicable[id] = value
		}
	}
	normalized, err := api.NormalizeParameters(schema, applicable)
	if err != nil {
		return ViewBandpass{}, err
	}
	low, lowOK := normalized[BandpassLowParameter].(float64)
	high, highOK := normalized[BandpassHighParameter].(float64)
	if !lowOK || !highOK {
		return ViewBandpass{}, errors.New("bandpass requires numeric frequencies in Hz")
	}
	p := ViewBandpass{
		LowHz: low, HighHz: high,
		Response:      FilterResponse(normalized[FilterResponseParameter].(string)),
		Method:        FilterMethod(normalized[FilterMethodParameter].(string)),
		Order:         int(normalized[FilterOrderParameter].(int64)),
		Phase:         FilterPhase(normalized[FilterPhaseParameter].(string)),
		RippleDB:      normalized[FilterRippleParameter].(float64),
		AttenuationDB: normalized[FilterAttenuationParameter].(float64),
	}.Canonical()
	if p.Response == FilterLegacyBandpass && p.Method == FilterButterworth && p.Order == 4 && p.Phase == FilterZeroPhase {
		p = ViewBandpass{LowHz: p.LowHz, HighHz: p.HighHz}
	}
	return p, p.Validate(dtUS)
}

func FilterParameterApplies(id string, response FilterResponse, method FilterMethod) bool {
	switch id {
	case BandpassHighParameter:
		return response != FilterLowpass && response != FilterHighpass
	case FilterRippleParameter:
		return method == FilterChebyshevI || method == FilterElliptic
	case FilterAttenuationParameter:
		return method == FilterChebyshevII || method == FilterElliptic
	default:
		return true
	}
}

type BandpassContext struct {
	Fingerprint string
	Scope       api.AlgorithmProcessingScope
}

// WriteBandpassArtifact uses the captured visible artifact for the output
// contract, but reads each selected source trace's full real temporal context.
// It never reads A_view or B, and never holds a complete seismic volume.
// Only the permitted visible window is published to the B artifact.
func (in *AlgorithmSectionInput) WriteBandpassArtifact(ctx context.Context, localInputPath, outputPath string, p ViewBandpass, progress func(done, total int)) (BandpassContext, error) {
	var out BandpassContext
	if ctx == nil {
		ctx = context.Background()
	}
	if err := in.SourceUnchanged(); err != nil {
		return out, err
	}
	input, err := artifact.OpenContext(ctx, localInputPath)
	if err != nil {
		return out, err
	}
	defer input.Close()
	if input.Fingerprint() != in.inputFingerprint {
		return out, errors.New("bandpass captured visible input changed")
	}
	f, err := segy.Open(in.request.SourcePath)
	if err != nil {
		return out, err
	}
	defer f.Close()
	design, err := p.design(f.Info.SampleIntervalUS)
	if err != nil {
		return out, err
	}
	if f.Info.SamplesPerTrace < design.minimum {
		return out, fmt.Errorf("filter requires at least %d real context samples", design.minimum)
	}
	if err := in.SourceUnchanged(); err != nil {
		return out, err
	}
	scope := api.AlgorithmProcessingScope{TraceCount: int64(len(in.request.TraceIndices)),
		SampleStart: int64(in.request.SampleStart), SampleEnd: int64(in.request.SampleEnd),
		ContextStart: 0, ContextEnd: int64(f.Info.SamplesPerTrace - 1), SampleIntervalUS: int64(f.Info.SampleIntervalUS)}
	hash := sha256.New()
	_, _ = hash.Write([]byte("seisforge.bandpass.real-context.v1\x00"))
	var word [8]byte
	for _, v := range []int64{scope.TraceCount, scope.ContextEnd, scope.SampleIntervalUS} {
		binary.LittleEndian.PutUint64(word[:], uint64(v))
		_, _ = hash.Write(word[:])
	}
	err = artifact.Write(ctx, outputPath, input.Metadata(), input.TraceMetadata(), func(row int64) ([]float64, []bool, error) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := in.SourceUnchanged(); err != nil {
			return nil, nil, err
		}
		trace := in.request.TraceIndices[row]
		header, err := f.ReadTraceHeader(trace)
		if err != nil {
			return nil, nil, err
		}
		if (header.SampleCount != 0 && header.SampleCount != f.Info.SamplesPerTrace) ||
			(header.SampleIntervalUS != 0 && header.SampleIntervalUS != f.Info.SampleIntervalUS) {
			return nil, nil, fmt.Errorf("A trace %d has inconsistent temporal context", trace+1)
		}
		full, err := f.ReadTraceWindow(trace, 0, f.Info.SamplesPerTrace-1)
		if err != nil {
			return nil, nil, err
		}
		visible, _, err := input.ReadTraceWindow(row, 0, input.SamplesPerTrace())
		if err != nil {
			return nil, nil, err
		}
		for i, value := range visible {
			if math.Float64bits(value) != math.Float64bits(full[in.request.SampleStart+i]) {
				return nil, nil, errors.New("A samples changed after the immutable task snapshot")
			}
		}
		binary.LittleEndian.PutUint64(word[:], uint64(trace))
		_, _ = hash.Write(word[:])
		_, _ = hash.Write(header.Raw[:])
		for _, value := range full {
			binary.LittleEndian.PutUint64(word[:], math.Float64bits(value))
			_, _ = hash.Write(word[:])
		}
		filtered, err := design.filterTrace(ctx, full)
		if err != nil {
			return nil, nil, fmt.Errorf("A trace %d: %w", trace+1, err)
		}
		if progress != nil {
			progress(int(row)+1, len(in.request.TraceIndices))
		}
		if row == scope.TraceCount-1 {
			if err := in.SourceUnchanged(); err != nil {
				return nil, nil, err
			}
		}
		return filtered[in.request.SampleStart : in.request.SampleEnd+1], nil, nil
	})
	if err != nil {
		return out, err
	}
	// Write published this new, task-owned path without replacing another
	// file. A late failure must not leave a valid-looking result behind.
	removeFailedOutput := func(err error) error {
		if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			return errors.Join(err, fmt.Errorf("remove rejected filter output: %w", cleanupErr))
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return out, removeFailedOutput(err)
	}
	if err := in.SourceUnchanged(); err != nil {
		return out, removeFailedOutput(err)
	}
	out.Fingerprint, out.Scope = hex.EncodeToString(hash.Sum(nil)), scope
	return out, nil
}
