//go:build windows

package main

// This file contains the small, UI-independent parser used by the prestack
// gather toolbar when the user requests more than one bin.  The parser only
// resolves text to the already indexed keys; it never invents a key or reads
// the SEG-Y file.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

var (
	prestackMultiNumber = `(?:#?[-+]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))`
	prestackMultiRange  = regexp.MustCompile(`^(` + prestackMultiNumber + `)(?:\.\.|-)(` + prestackMultiNumber + `)$`)
	prestackMultiXY     = regexp.MustCompile(`^(-?[0-9]+)\s*[,/:]\s*(-?[0-9]+)$`)
)

// parsePrestackMultiKeyCommand resolves a compact key expression against the
// complete key list currently shown by the native combo.  Accepted forms:
//
//	all / 全部       the synthetic all-range key, when present
//	1001,1003       individual ID or offset-bin centre values
//	1001-1005       inclusive ID/offset-bin range
//	#2,#5-#8        one-based positions in the available-key list
//	3:4              CMP XY bin indices (x:y, x/y and x,x are accepted)
//
// Commas, semicolons and whitespace separate expressions.  The returned keys
// are unique and retain expression order.  Numeric ranges use the indexed
// keys that fall inside the endpoints, so sparse IDs and negative offsets are
// handled without manufacturing missing bins.
func parsePrestackMultiKeyCommand(input string, available []prestackcore.GatherKey) ([]prestackcore.GatherKey, error) {
	input = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(input, "，", ","), "；", ";"))
	if input == "" {
		return nil, fmt.Errorf("请输入道集键值，例如 1001,1003-1005")
	}
	allIndex := -1
	for i, k := range available {
		if k.All {
			allIndex = i
			break
		}
	}
	seen := make(map[string]struct{})
	result := make([]prestackcore.GatherKey, 0, 8)
	add := func(k prestackcore.GatherKey) {
		key := k.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		result = append(result, k)
	}
	// Keep a separator between signs and values intact: offset expressions
	// such as -40--20 must remain one range token.
	parts := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		lower := strings.ToLower(raw)
		if lower == "all" || lower == "全部" || lower == "全部范围" {
			if allIndex < 0 {
				return nil, fmt.Errorf("当前道集没有全部范围键")
			}
			add(available[allIndex])
			continue
		}
		if strings.HasPrefix(raw, "#") {
			if err := addPositionExpression(raw, available, add); err != nil {
				return nil, err
			}
			continue
		}
		if m := prestackMultiXY.FindStringSubmatch(raw); len(m) == 3 {
			x, _ := strconv.ParseInt(m[1], 10, 64)
			y, _ := strconv.ParseInt(m[2], 10, 64)
			found := false
			for _, k := range available {
				if k.CMPBin && k.CMPBinXIndex == x && k.CMPBinYIndex == y {
					add(k)
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("未找到 CMP 分箱 %s", raw)
			}
			continue
		}
		if m := prestackMultiRange.FindStringSubmatch(raw); len(m) == 3 {
			lo, err1 := parseMultiNumber(m[1])
			hi, err2 := parseMultiNumber(m[2])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("无法解析范围 %q", raw)
			}
			if lo > hi {
				lo, hi = hi, lo
			}
			if err := addNumericRange(lo, hi, available, add); err != nil {
				return nil, err
			}
			continue
		}
		v, err := parseMultiNumber(raw)
		if err != nil {
			return nil, fmt.Errorf("无法解析键值 %q", raw)
		}
		if err := addNumericRange(v, v, available, add); err != nil {
			return nil, err
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("没有匹配的道集键值")
	}
	return result, nil
}

func parseMultiNumber(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(s), "#"), 64)
}

func addPositionExpression(raw string, available []prestackcore.GatherKey, add func(prestackcore.GatherKey)) error {
	if m := prestackMultiRange.FindStringSubmatch(raw); len(m) == 3 {
		a, err1 := strconv.Atoi(strings.TrimPrefix(m[1], "#"))
		b, err2 := strconv.Atoi(strings.TrimPrefix(m[2], "#"))
		if err1 != nil || err2 != nil {
			return fmt.Errorf("无法解析键值位置 %q", raw)
		}
		if a > b {
			a, b = b, a
		}
		if a < 1 || b > len(available) {
			return fmt.Errorf("键值位置 %q 超出 1..%d", raw, len(available))
		}
		for i := a; i <= b; i++ {
			add(available[i-1])
		}
		return nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(raw, "#"))
	if err != nil || n < 1 || n > len(available) {
		return fmt.Errorf("键值位置 %q 超出 1..%d", raw, len(available))
	}
	add(available[n-1])
	return nil
}

func addNumericRange(lo, hi float64, available []prestackcore.GatherKey, add func(prestackcore.GatherKey)) error {
	matched := make([]int, 0, 8)
	for i, k := range available {
		if k.All {
			continue
		}
		v, ok := prestackKeyNumericValue(k)
		if ok && v >= lo && v <= hi {
			matched = append(matched, i)
		}
	}
	if len(matched) == 0 {
		return fmt.Errorf("范围 %.6g..%.6g 没有匹配键值", lo, hi)
	}
	// Preserve the supplied key order, which is the stable order used by the
	// combo (CMP bins/offset bins may not be numerically contiguous).
	for _, i := range matched {
		add(available[i])
	}
	return nil
}

func prestackKeyNumericValue(k prestackcore.GatherKey) (float64, bool) {
	if k.OffsetBin {
		return k.OffsetCenter, true
	}
	if k.AzimuthBin {
		return k.AzimuthCenter, true
	}
	if k.CMPBin || k.Grid || k.Coordinate || k.Raw {
		return 0, false
	}
	return float64(k.ID), true
}

// sortPrestackKeysByExpression is useful when a caller wants a deterministic
// canonical order after parsing an expression.  The UI normally leaves the
// expression order intact, so this is deliberately not called by the parser.
func sortPrestackKeysByExpression(keys []prestackcore.GatherKey) {
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
}
