package algorithmapi

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func pointer(v float64) *float64 { return &v }
func descriptor() AlgorithmDescriptor {
	return AlgorithmDescriptor{ID: "demo.noop", Version: "1.0.0", Name: "Noop", ProtocolVersion: ProtocolVersion, InputKinds: []string{"section"}, OutputKinds: []string{"section"}, Executable: "demo.exe", MinimumHostVersion: "1.10.4"}
}
func schema() []ParameterDescriptor {
	return []ParameterDescriptor{
		{ID: "rank", Type: "integer", Default: json.Number("2"), Min: pointer(1), Max: pointer(10), Step: pointer(1)},
		{ID: "gain", Type: "number", Default: 1.5, Min: pointer(0), Max: pointer(10)},
		{ID: "enabled", Type: "boolean", Default: true},
		{ID: "label", Type: "string", Default: "demo"},
		{ID: "mode", Type: "enum", Options: []string{"soft", "hard"}, Default: "hard"},
		{ID: "file", Type: "file", Default: "input file.dat"},
		{ID: "window", Type: "range", Default: map[string]any{"min": 0.0, "max": 10.0}, Min: pointer(0), Max: pointer(20)},
		{ID: "advanced", Type: "group", Parameters: []ParameterDescriptor{{ID: "seed", Type: "integer", Default: 4}}},
	}
}
func TestAllParameterTypesAndDefensiveDefaults(t *testing.T) {
	s := schema()
	v, err := NormalizeParameters(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["rank"] != int64(2) || v["gain"] != 1.5 || v["window"] != (RangeValue{0, 10}) {
		t.Fatalf("bad defaults %#v", v)
	}
	v["advanced"].(map[string]any)["seed"] = 99
	w, err := NormalizeParameters(s, nil)
	if err != nil || w["advanced"].(map[string]any)["seed"] != int64(4) {
		t.Fatalf("default aliased: %v %v", w, err)
	}
	d := descriptor()
	d.Parameters = s
	if err = ValidateDescriptor(d, "1.10.4-rc1"); err != nil {
		t.Fatal(err)
	}
	c := CloneDescriptor(d)
	c.Parameters[4].Options[0] = "changed"
	if d.Parameters[4].Options[0] != "soft" {
		t.Fatal("descriptor clone aliases options")
	}
}
func TestInvalidParameters(t *testing.T) {
	for _, v := range []map[string]any{{"unknown": 1}, {"rank": 1.2}, {"rank": 11}, {"rank": int64(math.MaxInt64)}, {"gain": math.NaN()}, {"gain": math.Inf(1)}, {"enabled": "true"}, {"mode": "other"}, {"file": "x\x00y"}, {"window": map[string]any{"min": 8, "max": 4}}, {"window": map[string]any{"min": 0, "max": 3, "extra": 2}}, {"advanced": map[string]any{"extra": true}}} {
		if _, err := NormalizeParameters(schema(), v); err == nil {
			t.Fatalf("accepted %#v", v)
		}
	}
	if _, err := NormalizeParameters([]ParameterDescriptor{{ID: "x", Type: "string", Required: true}}, nil); err == nil {
		t.Fatal("missing required accepted")
	}
	if _, err := NormalizeParameters([]ParameterDescriptor{{ID: "x", Type: "integer", Default: "bad"}}, nil); err == nil {
		t.Fatal("invalid default accepted")
	}
}
func TestManifestStrictAndVersioned(t *testing.T) {
	d := descriptor()
	b, _ := json.Marshal(d)
	parsed, err := ParseManifest(b, "1.10.4")
	if err != nil || !reflect.DeepEqual(parsed, d) {
		t.Fatalf("round trip %v", err)
	}
	if _, err = ParseManifest(b, "1.9.9"); err == nil {
		t.Fatal("newer host requirement ignored")
	}
	for _, b := range [][]byte{append(b, []byte("{}")...), []byte(`{"unknown":1}`), make([]byte, 1<<20+1)} {
		if _, err = ParseManifest(b, "1.10.4"); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	d.ProtocolVersion = "future"
	if ValidateDescriptor(d, "1.10.4") == nil {
		t.Fatal("unknown protocol accepted")
	}
}

func TestParameterUIManifestAndNormalization(t *testing.T) {
	d := descriptor()
	d.Parameters = []ParameterDescriptor{
		{ID: "time_window", Type: "integer", Default: json.Number("0"), Min: pointer(0), Description: "0 uses all input samples", UI: &ParameterUI{Section: ParameterUICommon, Order: 1}},
		{ID: "output_sample_block", Type: "integer", Default: json.Number("0"), Min: pointer(0), Description: "0 uses the effective time window", UI: &ParameterUI{Section: ParameterUIAdvanced}},
		{ID: "unannotated", Type: "boolean", Default: true},
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseManifest(b, "1.10.4")
	if err != nil || !reflect.DeepEqual(parsed, d) {
		t.Fatalf("presentation hints did not round trip: %#v, %v", parsed, err)
	}
	copy := CloneDescriptor(parsed)
	copy.Parameters[0].UI.Order = 3
	if parsed.Parameters[0].UI.Order != 1 {
		t.Fatal("cloning the descriptor aliases its UI metadata")
	}
	withUI, err := NormalizeParameters(parsed.Parameters, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range copy.Parameters {
		copy.Parameters[i].UI = nil
	}
	withoutUI, err := NormalizeParameters(copy.Parameters, nil)
	if err != nil || !reflect.DeepEqual(withUI, withoutUI) || withUI["time_window"] != int64(0) || withUI["output_sample_block"] != int64(0) {
		t.Fatalf("presentation changed normalized defaults: %v vs %v, %v", withUI, withoutUI, err)
	}
}

func TestInvalidParameterUIRejected(t *testing.T) {
	for _, ui := range []ParameterUI{{Section: ""}, {Section: "hidden"}, {Section: ParameterUICommon, Order: -1}, {Section: ParameterUIAdvanced, Order: 256}} {
		d := descriptor()
		d.Parameters = []ParameterDescriptor{{ID: "value", Type: "integer", UI: &ui}}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ParseManifest(b, "1.10.4"); err == nil {
			t.Errorf("accepted invalid parameter presentation: %+v", ui)
		}
	}
}

func TestParameterStepValidation(t *testing.T) {
	tests := []struct {
		name           string
		p              ParameterDescriptor
		valid, invalid []any
	}{
		{"odd integers", ParameterDescriptor{ID: "v", Type: "integer", Min: pointer(3), Step: pointer(2)}, []any{3, 5, 9, json.Number("11")}, []any{4, 8, 10}},
		{"integer negative anchor", ParameterDescriptor{ID: "v", Type: "integer", Min: pointer(-5), Step: pointer(3)}, []any{-5, -2, 1, 4}, []any{-4, 0, 3}},
		{"integer zero anchor", ParameterDescriptor{ID: "v", Type: "integer", Step: pointer(2)}, []any{-4, 0, 8}, []any{-3, 3}},
		{"integer exact large difference", ParameterDescriptor{ID: "v", Type: "integer", Min: pointer(-9007199254740991), Step: pointer(3)}, []any{int64(9007199254740991) - 2}, []any{int64(9007199254740991) - 1}},
		{"floating increment", ParameterDescriptor{ID: "v", Type: "number", Min: pointer(0.1), Step: pointer(0.1)}, []any{0.1, 0.3, 0.7, json.Number("1.1")}, []any{0.15, 0.35, 0.300001}},
		{"floating negative anchor", ParameterDescriptor{ID: "v", Type: "number", Min: pointer(-0.3), Step: pointer(0.1)}, []any{-0.3, -0.1, 0.0, 0.3}, []any{-0.25, 0.05}},
		{"floating zero anchor", ParameterDescriptor{ID: "v", Type: "number", Step: pointer(0.2)}, []any{-0.6, 0.0, 0.6}, []any{-0.1, 0.3}},
		{"large quotient half step", ParameterDescriptor{ID: "v", Type: "number", Step: pointer(1)}, []any{1000000000000.0}, []any{1000000000000.5}},
		{"range endpoints", ParameterDescriptor{ID: "v", Type: "range", Min: pointer(0.1), Step: pointer(0.1)}, []any{RangeValue{0.3, 0.7}, map[string]any{"min": 0.1, "max": 0.3}}, []any{RangeValue{0.35, 0.7}, RangeValue{0.3, 0.75}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, input := range tt.valid {
				values := map[string]any{"v": input}
				before, _ := json.Marshal(values)
				if _, err := NormalizeParameters([]ParameterDescriptor{tt.p}, values); err != nil {
					t.Fatalf("valid %v: %v", input, err)
				}
				after, _ := json.Marshal(values)
				if string(before) != string(after) {
					t.Fatal("normalization mutated caller values")
				}
			}
			for _, input := range tt.invalid {
				if _, err := NormalizeParameters([]ParameterDescriptor{tt.p}, map[string]any{"v": input}); err == nil {
					t.Fatalf("off-grid %v accepted", input)
				}
			}
		})
	}
	p := ParameterDescriptor{ID: "v", Type: "integer", Default: 8, Min: pointer(3), Step: pointer(2)}
	if _, err := NormalizeParameters([]ParameterDescriptor{p}, nil); err == nil {
		t.Fatal("off-grid default accepted")
	}
	p.Default = 9
	if values, err := NormalizeParameters([]ParameterDescriptor{p}, nil); err != nil || values["v"] != int64(9) {
		t.Fatalf("valid default: %v %v", values, err)
	}
}
