//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	mappingprofile "github.com/DavidLiu-code/SeisForge-Studio/internal/mappingprofile"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

var prestackMappingProfiles []mappingprofile.Profile

func prestackMappingProfilesPath() string {
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "SeisForge Studio", "prestack_mapping_profiles.json")
	}
	return filepath.Join(os.TempDir(), "SeisForge Studio", "prestack_mapping_profiles.json")
}

func loadPrestackMappingProfiles() {
	profiles, err := mappingprofile.Load(prestackMappingProfilesPath())
	if err != nil {
		prestackMappingProfiles = nil
		return
	}
	prestackMappingProfiles = mappingprofile.CloneProfiles(profiles)
	refreshPrestackMappingProfileCombo("")
}

func refreshPrestackMappingProfileCombo(selected string) {
	if prestackUI.mappingProfile == 0 {
		return
	}
	pSendMessageW.Call(prestackUI.mappingProfile, CB_RESETCONTENT, 0, 0)
	selectedIndex := -1
	for i, profile := range prestackMappingProfiles {
		pSendMessageW.Call(prestackUI.mappingProfile, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(profile.Name))))
		if strings.EqualFold(profile.Name, selected) {
			selectedIndex = i
		}
	}
	if selectedIndex >= 0 {
		pSendMessageW.Call(prestackUI.mappingProfile, CB_SETCURSEL, uintptr(selectedIndex), 0)
	} else if len(prestackMappingProfiles) > 0 {
		pSendMessageW.Call(prestackUI.mappingProfile, CB_SETCURSEL, 0, 0)
	} else {
		pSendMessageW.Call(prestackUI.mappingProfile, CB_SETCURSEL, ^uintptr(0), 0)
	}
}

func prestackCurrentMappingProfileTarget() mappingprofile.Target {
	if prestackState.mappingTargetB {
		return mappingprofile.TargetB
	}
	return mappingprofile.TargetA
}

func prestackCurrentMappingProfileMapping() prestackcore.HeaderMapping {
	if prestackState.mappingTargetB {
		return prestackState.compareBMapping
	}
	return prestackState.mapping
}

func prestackCurrentMappingProfileQuality() mappingprofile.QualitySummary {
	detection := prestackState.detection
	if prestackState.mappingTargetB {
		detection = prestackState.compareBDetection
	}
	fields := make([]mappingprofile.FieldSummary, 0, len(detection.Candidates))
	sampled := detection.SampledTraces
	if sampled < 0 {
		sampled = 0
	}
	for _, candidate := range detection.Candidates {
		present := int(candidate.Coverage*float64(sampled) + 0.5)
		fields = append(fields, mappingprofile.FieldSummary{
			Field: candidate.Field, Total: sampled, Present: present,
			Coverage: candidate.Coverage,
		})
	}
	return mappingprofile.QualitySummary{
		SampledTraces: sampled, ValidHeaders: sampled, Fields: fields,
		Confidence: detection.Confidence, Warnings: append([]string(nil), detection.Warnings...),
	}
}

func savePrestackMappingProfile() {
	name := strings.TrimSpace(getText(prestackUI.mappingProfileName))
	if name == "" {
		message(prestackHwnd, "保存 Mapping Profile", "请输入 Profile 名称。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if prestackState.mappingTargetB && prestackState.compareBDataset == nil {
		message(prestackHwnd, "保存 Mapping Profile", "B 尚未加载，不能保存 B Profile。", MB_OK|MB_ICONINFORMATION)
		return
	}
	profile, err := mappingprofile.New(name, prestackCurrentMappingProfileTarget(), prestackCurrentMappingProfileMapping(), prestackCurrentMappingProfileQuality())
	if err != nil {
		message(prestackHwnd, "保存 Mapping Profile", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	profiles, err := mappingprofile.Upsert(prestackMappingProfiles, profile, true)
	if err != nil {
		message(prestackHwnd, "保存 Mapping Profile", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	if err := mappingprofile.Save(prestackMappingProfilesPath(), profiles, true); err != nil {
		message(prestackHwnd, "保存 Mapping Profile", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	prestackMappingProfiles = profiles
	refreshPrestackMappingProfileCombo(profile.Name)
	setPrestackStatus(fmt.Sprintf("已保存 %s Mapping Profile：%s", profile.Target, profile.Name))
}

func applyPrestackMappingProfile() {
	selected, _, _ := pSendMessageW.Call(prestackUI.mappingProfile, CB_GETCURSEL, 0, 0)
	if int(selected) < 0 || int(selected) >= len(prestackMappingProfiles) {
		message(prestackHwnd, "应用 Mapping Profile", "请先选择一个 Profile。", MB_OK|MB_ICONINFORMATION)
		return
	}
	profile := prestackMappingProfiles[int(selected)].Clone()
	if profile.Target != prestackCurrentMappingProfileTarget() {
		message(prestackHwnd, "应用 Mapping Profile", "Profile 目标与当前 Mapping 目标不同。", MB_OK|MB_ICONINFORMATION)
		return
	}
	for i, field := range prestackMappingFields(&profile.Mapping) {
		if i < len(prestackUI.mappingEdits) {
			setText(prestackUI.mappingEdits[i], strconv.Itoa(*field.value))
		}
	}
	applyPrestackMapping()
	setPrestackStatus(fmt.Sprintf("已应用 %s Mapping Profile：%s", profile.Target, profile.Name))
}
