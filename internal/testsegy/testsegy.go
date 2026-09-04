// Package testsegy creates compact SEG-Y fixtures for internal package tests.
package testsegy

import (
	"encoding/binary"
	"math"
	"os"
)

type Options struct {
	Rows                int
	Cols                int
	Samples             int
	RegularGrid         bool
	InlineByte          int
	CrosslineByte       int
	DuplicateInlineByte int
	CoordinateXByte     int
	CoordinateYByte     int
	CoordinateScalar    int16
	CoordinateUnits     int16
	Coordinates         []TraceCoordinate
}

type TraceCoordinate struct {
	X, Y int32
	CDP  int32
}

func Write(path string, options Options) error {
	rows, cols, samples := options.Rows, options.Cols, options.Samples
	if rows < 1 {
		rows = 1
	}
	if cols < 1 {
		cols = 1
	}
	if samples < 1 {
		samples = 4
	}
	inlineByte, crosslineByte := options.InlineByte, options.CrosslineByte
	if inlineByte == 0 {
		inlineByte = 189
	}
	if crosslineByte == 0 {
		crosslineByte = 193
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], uint16(samples))
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(header); err != nil {
		return err
	}
	traceIndex := 0
	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			trace := make([]byte, 240+samples*4)
			if options.RegularGrid {
				binary.BigEndian.PutUint32(trace[inlineByte-1:inlineByte+3], uint32(1000+row))
				binary.BigEndian.PutUint32(trace[crosslineByte-1:crosslineByte+3], uint32(2000+col))
				if options.DuplicateInlineByte > 0 {
					duplicate := options.DuplicateInlineByte
					binary.BigEndian.PutUint32(trace[duplicate-1:duplicate+3], uint32(1000+row))
				}
			}
			if traceIndex < len(options.Coordinates) {
				xByte, yByte := options.CoordinateXByte, options.CoordinateYByte
				if xByte == 0 {
					xByte = 181
				}
				if yByte == 0 {
					yByte = 185
				}
				coordinate := options.Coordinates[traceIndex]
				binary.BigEndian.PutUint32(trace[xByte-1:xByte+3], uint32(coordinate.X))
				binary.BigEndian.PutUint32(trace[yByte-1:yByte+3], uint32(coordinate.Y))
				binary.BigEndian.PutUint32(trace[20:24], uint32(coordinate.CDP))
				binary.BigEndian.PutUint16(trace[70:72], uint16(options.CoordinateScalar))
				binary.BigEndian.PutUint16(trace[88:90], uint16(options.CoordinateUnits))
			}
			for sample := 0; sample < samples; sample++ {
				value := float32((row*cols+col)*100 + sample)
				binary.BigEndian.PutUint32(trace[240+sample*4:244+sample*4], math.Float32bits(value))
			}
			if _, err := f.Write(trace); err != nil {
				return err
			}
			traceIndex++
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
