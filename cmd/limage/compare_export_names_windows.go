//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// Leave room for the adjacent, verified temporary output and its suffix. This
// also keeps the ordinary Windows Save dialog and Explorer paths usable.
const numericalExportPathLimit = 232

var numericalExportLogMu sync.Mutex

func logNumericalExportError(err error) {
	log.Printf("numerical SEG-Y export: %v", err)
	numericalExportLogMu.Lock()
	defer numericalExportLogMu.Unlock()
	dir := licenseDirectory()
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	path := filepath.Join(dir, "numerical_export_errors.log")
	const capacity = 512 << 10
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() {
			return
		}
		if st.Size() >= capacity {
			prior := filepath.Join(dir, "numerical_export_errors.previous.log")
			if ok, _, _ := displayMoveFile.Call(uintptr(unsafe.Pointer(u16(path))), uintptr(unsafe.Pointer(u16(prior))), 0x9); ok == 0 {
				return
			}
		}
	} else if !errors.Is(e, fs.ErrNotExist) {
		return
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if e != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %v\r\n", time.Now().Format(time.RFC3339), err)
}

func numericalExportUTF16Len(v string) int { return len(utf16.Encode([]rune(v))) }

func truncateNumericalExportName(name string, maxUnits int) string {
	if maxUnits < 1 {
		return ""
	}
	units := 0
	for i, r := range name {
		units++
		if r > 0xffff {
			units++
		}
		if units > maxUnits {
			return name[:i]
		}
	}
	return name
}

func numericalExportInvalidChar(r rune) bool {
	return r < 32 || strings.ContainsRune(`<>:"/\|?*`, r)
}

func numericalExportReservedName(s string) bool {
	stem, _, _ := strings.Cut(s, ".")
	s = strings.ToUpper(strings.TrimRight(stem, " "))
	if s == "CON" || s == "PRN" || s == "AUX" || s == "NUL" || s == "CONIN$" || s == "CONOUT$" {
		return true
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if tail, ok := strings.CutPrefix(s, prefix); ok && len([]rune(tail)) == 1 && strings.Contains("123456789¹²³", tail) {
			return true
		}
	}
	return false
}

func cleanNumericalExportStem(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if numericalExportInvalidChar(r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, " .")
	if s == "" {
		s = "section"
	}
	if numericalExportReservedName(s) {
		s = "_" + s
	}
	return strings.TrimRight(truncateNumericalExportName(s, limit), " .")
}

func numericalExportInputStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func numericalExportFilterTag(p poststack.ViewBandpass) string {
	p = p.Canonical()
	response := map[poststack.FilterResponse]string{poststack.FilterLowpass: "LP", poststack.FilterHighpass: "HP",
		poststack.FilterBandpass: "BP", poststack.FilterBandstop: "BS", poststack.FilterLegacyBandpass: "BPCompat"}[p.Response]
	method := map[poststack.FilterMethod]string{poststack.FilterButterworth: "BW", poststack.FilterChebyshevI: "C1",
		poststack.FilterChebyshevII: "C2", poststack.FilterElliptic: "EL"}[p.Method]
	num := func(f float64) string { return strings.ReplaceAll(strconv.FormatFloat(f, 'g', -1, 64), ".", "p") }
	corner := num(p.LowHz)
	if p.HighHz > 0 {
		corner += "-" + num(p.HighHz)
	}
	phase := "Z"
	if p.Phase == poststack.FilterForward {
		phase = "F"
	}
	more := ""
	if p.RippleDB > 0 {
		more += "r" + num(p.RippleDB)
	}
	if p.AttenuationDB > 0 {
		more += "a" + num(p.AttenuationDB)
	}
	return fmt.Sprintf("%s%sHz%s%d%s%s", response, corner, method, p.Order, phase, more)
}

func numericalExportTimeToken(us int64) string {
	// Format the integer grid origin exactly, without reading short labels or
	// introducing decimal noise via a floating-point ms conversion.
	text := strconv.FormatInt(us/1000, 10)
	if us < 0 && us > -1000 {
		text = "-0"
	}
	if rem := us % 1000; rem != 0 {
		if rem < 0 {
			rem = -rem
		}
		text += "p" + strings.TrimRight(fmt.Sprintf("%03d", rem), "0")
	}
	return text
}

func numericalExportSpan(prefix string, first, last int32) string {
	text := prefix + strconv.FormatInt(int64(first), 10)
	if first != last {
		text += "-" + strconv.FormatInt(int64(last), 10)
	}
	return text
}

func numericalExportNameParts(p compareExportPlan) (stem, description string) {
	c, q := p.capture, p.request
	ref := 0
	if q.kind == compareExportB {
		ref = 1
	}
	source := c.sources[ref]
	stem = source.originalStem
	if stem == "" {
		stem = numericalExportInputStem(source.path)
	}
	fields := []string{[]string{"A", "B", "A-B"}[q.kind]}
	for side, s := range c.sources {
		if (side == 0 && q.kind == compareExportB) || (side == 1 && q.kind == compareExportA) {
			continue
		}
		tag := s.processTag
		if q.effective && c.settings[side].Enabled {
			tag = strings.TrimPrefix(tag+"+V"+numericalExportFilterTag(c.settings[side].Params), "+")
		} else if !q.effective && c.settings[side].Enabled {
			tag = strings.TrimPrefix(tag+"+Base", "+")
		}
		if tag != "" {
			if q.kind == compareExportDifference {
				tag = string(rune('A'+side)) + "-" + tag
			}
			fields = append(fields, tag)
		}
	}
	if p.mapped == 0 {
		if len(p.rows) == 0 {
			fields = append(fields, "Traces0")
		} else {
			first, last := p.rows[0].pick.trace, p.rows[0].pick.trace
			for _, row := range p.rows[1:] {
				first, last = min(first, row.pick.trace), max(last, row.pick.trace)
			}
			fields = append(fields, fmt.Sprintf("Tr%d-%d", first+1, last+1))
		}
	} else if q.scope == compareExportAll && !c.traceOrder {
		fields = append(fields, numericalExportSpan("IL", p.il0, p.il1), numericalExportSpan("XL", p.xl0, p.xl1))
	} else {
		axis := "XL"
		if c.traceOrder {
			axis = "Tr"
		} else if c.key.mode == 1 {
			axis = "IL"
		}
		fields = append(fields, numericalExportSpan(axis, p.first, p.last))
	}
	fields = append(fields, "T"+numericalExportTimeToken(p.t0)+"-"+numericalExportTimeToken(p.t1)+"ms")
	return stem, strings.Join(fields, "_")
}

func numericalExportBasePath(dir string, p compareExportPlan) (string, error) {
	stem, description := numericalExportNameParts(p)
	suffix := "_" + description + ".sgy"
	budget := numericalExportPathLimit - numericalExportUTF16Len(filepath.Join(dir, suffix)) - 5 // _000, etc.
	if budget < 5 {
		return "", errors.New("保存目录太长，请选择较短的位置")
	}
	return filepath.Join(dir, cleanNumericalExportStem(stem, min(60, budget))+suffix), nil
}

func availableNumericalExportPath(base string) (string, error) {
	extension := filepath.Ext(base)
	stem := strings.TrimSuffix(base, extension)
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s_%03d%s", stem, i, extension)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("连续的导出名称已被占用，请另取文件名或保存目录")
}

func resolveNumericalExportPath(text, dir string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("请输入输出文件名")
	}
	if !filepath.IsAbs(text) {
		if filepath.VolumeName(text) != "" || strings.HasPrefix(text, "\\") || strings.HasPrefix(text, "/") {
			return "", errors.New("请输入完整保存路径，或仅输入文件名")
		}
		text = filepath.Join(dir, text)
	}
	if strings.HasPrefix(text, `\\.\`) {
		return "", errors.New("输出路径必须是普通文件")
	}
	if filepath.Ext(text) == "" {
		text += ".sgy"
	}
	if !segy.NumericalSEGYSuffix(text) {
		return "", errors.New("此入口只导出 SEG-Y 数值数据，请使用 .sgy 或 .segy")
	}
	text = filepath.Clean(text)
	for _, component := range strings.FieldsFunc(strings.TrimPrefix(text, filepath.VolumeName(text)), func(r rune) bool { return r == '\\' || r == '/' }) {
		if component != strings.TrimRight(component, " .") || strings.ContainsFunc(component, numericalExportInvalidChar) || numericalExportReservedName(component) {
			return "", errors.New("文件名或目录包含 Windows 不允许的字符或保留名称")
		}
	}
	if numericalExportUTF16Len(text) > numericalExportPathLimit {
		return "", errors.New("保存路径过长，请缩短文件名或选择较短的目录")
	}
	return text, nil
}

func numericalExportDirectory(source string) string {
	if data, err := os.ReadFile(numericalExportDirectoryPath()); err == nil && len(data) < 4096 {
		value := strings.TrimSpace(string(data))
		if st, err := os.Stat(value); filepath.IsAbs(value) && err == nil && st.IsDir() {
			return value
		}
	}
	if source != "" && filepath.IsAbs(source) {
		dir := filepath.Dir(source)
		rel, err := filepath.Rel(os.TempDir(), dir)
		segment, _, _ := strings.Cut(strings.ToLower(rel), string(filepath.Separator))
		internal := err == nil && (segment == "." || strings.HasPrefix(segment, "seisforge-algorithm-") || strings.HasPrefix(segment, "seisforge-poststack-result-"))
		if !internal {
			return dir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

func numericalExportDirectoryPath() string {
	return filepath.Join(licenseDirectory(), "numeric_export_folder.txt")
}

func rememberNumericalExportDirectory(dir string) {
	path := numericalExportDirectoryPath()
	if !filepath.IsAbs(dir) || os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), "numeric-export-folder-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(dir)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err != nil || closeErr != nil {
		return
	}
	if ok, _, err := displayMoveFile.Call(uintptr(unsafe.Pointer(u16(f.Name()))), uintptr(unsafe.Pointer(u16(path))), 0x9); ok == 0 {
		log.Printf("remember numerical export directory: %v", err)
	}
}
