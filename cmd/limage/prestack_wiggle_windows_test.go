//go:build windows

package main

import "testing"

func TestPrestackWiggleRasterWidthHonorsDecimation(t *testing.T) {
	tests := []struct {
		name       string
		traces     int
		sceneWidth int
		factor     int
		want       int
	}{
		{"dense", 5000, 1600, 1, 1024},
		{"two", 5000, 1600, 2, 800},
		{"four", 5000, 1600, 4, 400},
		{"default", 5000, 1600, 5, 320},
		{"eight", 5000, 1600, 8, 200},
		{"short gather", 37, 1600, 5, 37},
		{"narrow scene keeps two", 1, 1, 8, 1},
		{"invalid factor uses default", 5000, 1600, 0, 320},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prestackWiggleRasterWidth(tc.traces, tc.sceneWidth, tc.factor); got != tc.want {
				t.Fatalf("prestackWiggleRasterWidth(%d,%d,%d)=%d, want %d", tc.traces, tc.sceneWidth, tc.factor, got, tc.want)
			}
		})
	}
}

func TestPrestackWiggleRasterWidthEmptyGather(t *testing.T) {
	if got := prestackWiggleRasterWidth(0, 1600, 5); got != 0 {
		t.Fatalf("empty gather width=%d, want 0", got)
	}
}
