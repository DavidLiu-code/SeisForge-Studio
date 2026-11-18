//go:build windows

package main

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
)

func TestPublicBandpassAvailableAtActualApplicationVersion(t *testing.T) {
	r := registry.New(APP_VERSION)
	if err := r.RegisterBuiltin(poststack.BandpassDescriptor(2000)); err != nil {
		t.Fatalf("public bandpass unavailable in actual app %s: %v", APP_VERSION, err)
	}
	if _, err := r.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	m, ok := r.Get(poststack.BandpassAlgorithmID)
	if !ok || !m.IsBuiltin() || m.Descriptor().Name != "逐道时间滤波" {
		t.Fatal("missing public bandpass selector entry")
	}
	rows := algorithmParameterRows(m.Descriptor().Parameters, nil)
	markCommonAlgorithmParameters(rows)
	if len(rows) != 8 || !rows[0].Common || !rows[1].Common || !rows[2].Common || !rows[3].Common || rows[4].Common {
		t.Fatal("builtin did not use generic common-parameter form")
	}
}

func TestFormalFilterInapplicableFieldsDoNotConsumeLayout(t *testing.T) {
	rows := algorithmParameterRows(poststack.BandpassDescriptor(2000).Parameters, nil)
	markCommonAlgorithmParameters(rows)
	markAlgorithmFilterApplicability(rows, true, poststack.FilterLowpass, poststack.FilterChebyshevII)
	for i := range rows {
		rows[i].Value = uintptr(i + 1) // stand in for an unchanged edit control/draft
	}
	ids := func() []string {
		layout := layoutAlgorithmTabs(800, 485, rows, algorithmTabAdvanced, 0)
		found := []string{}
		for _, p := range append(layout.common, layout.parameters...) {
			found = append(found, rows[p.index].Descriptor.ID)
		}
		return found
	}
	before := ids()
	if slices.Contains(before, poststack.BandpassHighParameter) || slices.Contains(before, poststack.FilterRippleParameter) ||
		!slices.Contains(before, poststack.FilterAttenuationParameter) {
		t.Fatalf("incorrect low-pass Chebyshev II fields: %v", before)
	}
	markAlgorithmFilterApplicability(rows, true, poststack.FilterBandstop, poststack.FilterElliptic)
	after := ids()
	for _, id := range []string{poststack.BandpassHighParameter, poststack.FilterRippleParameter, poststack.FilterAttenuationParameter} {
		if !slices.Contains(after, id) {
			t.Fatalf("missing applicable Elliptic band-stop control %s: %v", id, after)
		}
	}
	for i := range rows {
		if rows[i].Value != uintptr(i+1) {
			t.Fatal("switching design replaced a draft control")
		}
	}
}

func TestFormalResponsePreviewUsesEntireSampleRateAndSelectedPhase(t *testing.T) {
	p := poststack.ViewBandpass{
		Response: poststack.FilterLowpass, Method: poststack.FilterButterworth,
		LowHz: 162.5, Order: 3, Phase: poststack.FilterForward,
	}
	single := makeTemporalResponseModel(2000, p, nil, "")
	p.Phase = poststack.FilterZeroPhase
	double := makeTemporalResponseModel(2000, p, nil, "")
	if single.err != nil || double.err != nil || single.order != 3 || double.order != 3 ||
		single.hz[len(single.hz)-1] != 250 || double.minimumSamples <= single.minimumSamples {
		t.Fatalf("unexpected single/two-pass response preview: %v / %v", single.err, double.err)
	}
	for i, value := range single.db {
		if !math.IsInf(value, -1) && math.Abs(2*value-double.db[i]) > 1e-10 {
			t.Fatalf("preview phases disagree at %g Hz", single.hz[i])
		}
	}
}
