package segy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ExportSampleFormat selects the on-disk SEG-Y sample encoding only.
type ExportSampleFormat uint16

const (
	ExportFormatSource ExportSampleFormat = 0
	ExportFormatIBM32  ExportSampleFormat = 1
	ExportFormatIEEE32 ExportSampleFormat = 5
	ExportFormatIEEE64 ExportSampleFormat = 6
)

var errIBM32NonFinite = errors.New("IBM Float32 cannot store a non-finite sample; choose IEEE Float32 or IEEE Float64")

// ResolveExportSampleFormat falls back to IEEE32 when the physical input
// does not provide one of the supported output formats.
func ResolveExportSampleFormat(request ExportSampleFormat, sourceCode int) (ExportSampleFormat, error) {
	switch request {
	case ExportFormatIBM32, ExportFormatIEEE32, ExportFormatIEEE64:
		return request, nil
	case ExportFormatSource:
		switch sourceCode {
		case int(ExportFormatIBM32), int(ExportFormatIEEE32), int(ExportFormatIEEE64):
			return ExportSampleFormat(sourceCode), nil
		default:
			return ExportFormatIEEE32, nil
		}
	default:
		return 0, fmt.Errorf("unsupported numerical SEG-Y output format %d", request)
	}
}

func encodeNumericalExportSamples(raw []byte, values []float64, order binary.ByteOrder, format ExportSampleFormat) error {
	switch format {
	case ExportFormatIBM32:
		for i, value := range values {
			word, err := encodeNumericalIBM32(value)
			if err != nil {
				return fmt.Errorf("sample %d: %w", i+1, err)
			}
			order.PutUint32(raw[i*4:], word)
		}
	case ExportFormatIEEE32:
		for i, value := range values {
			sample := float32(value)
			if math.IsNaN(value) || math.IsInf(value, 0) {
				sample = float32(math.NaN())
			} else if math.IsInf(float64(sample), 0) {
				return fmt.Errorf("sample %d exceeds the finite IEEE Float32 range; choose IEEE Float64", i+1)
			}
			order.PutUint32(raw[i*4:], math.Float32bits(sample))
		}
	case ExportFormatIEEE64:
		for i, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				value = math.NaN()
			}
			order.PutUint64(raw[i*8:], math.Float64bits(value))
		}
	}
	return nil
}

func encodeNumericalIBM32(value float64) (uint32, error) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, errIBM32NonFinite
	}
	sign := uint32(math.Float64bits(value)>>32) & 0x80000000
	if value == 0 {
		return sign, nil
	}
	fraction, exponent2 := math.Frexp(math.Abs(value))
	// The base-16 exponent normalizes the 24-bit fraction to [1/16, 1).
	exponent16 := (exponent2 + 3) >> 2
	mantissa := uint32(math.RoundToEven(math.Ldexp(fraction, exponent2+24-4*exponent16)))
	if mantissa == 0x1000000 {
		mantissa = 0x100000
		exponent16++
	}
	if exponent16 < -64 || exponent16 > 63 {
		return 0, errors.New("sample exceeds the representable IBM Float32 range; choose IEEE Float64")
	}
	return sign | uint32(exponent16+64)<<24 | mantissa, nil
}
