//go:build !windows

package segy

import (
	"errors"
	"os"
)

func platformMapReadOnly(file *os.File, size int64) ([]byte, func() error, error) {
	return nil, nil, errors.New("read-only SEG-Y mapping is unavailable on this platform")
}

func platformPrefetchMapped(data []byte, ranges []mappedReadRange) bool { return false }
