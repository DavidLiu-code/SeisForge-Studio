// Package dataset owns immutable SEG-Y identity and metadata. A dataset does
// not own UI state or a long-lived file handle; each workspace obtains its own
// independent reader with OpenReader.
package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type Metadata struct {
	FileSize            int64
	ModifiedAt          time.Time
	Endian              segy.Endian
	SampleIntervalUS    int
	SamplesPerTrace     int
	FormatCode          int
	BytesPerSample      int
	ExtendedTextHeaders int
	DataStart           int64
	TraceBytes          int64
	TraceCount          int64
}

type SeismicDataset struct {
	Path     string
	Metadata Metadata

	mu       sync.RWMutex
	geometry geometry.Geometry
}

func (d *SeismicDataset) Basename() string {
	if d == nil {
		return ""
	}
	return filepath.Base(d.Path)
}

// OpenReader returns a new *segy.File every time. Closing one workspace's
// reader therefore cannot invalidate another workspace's A/B/2-D/3-D reader.
func (d *SeismicDataset) OpenReader() (*segy.File, error) {
	if d == nil || d.Path == "" {
		return nil, fmt.Errorf("dataset path is empty")
	}
	return segy.Open(d.Path)
}

func (d *SeismicDataset) Geometry() geometry.Geometry {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.geometry
}

func (d *SeismicDataset) SetGeometry(g geometry.Geometry) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.geometry = g
	d.mu.Unlock()
}

type Manager struct{}

func NewManager() *Manager { return &Manager{} }

// Open validates the SEG-Y header, snapshots metadata and immediately closes
// the validation reader. No amplitude samples are copied into the dataset.
func (m *Manager) Open(path string) (*SeismicDataset, error) {
	if path == "" {
		return nil, fmt.Errorf("dataset path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve dataset path: %w", err)
	}
	abs = filepath.Clean(abs)
	reader, err := segy.Open(abs)
	if err != nil {
		return nil, err
	}
	info := reader.Info
	closeErr := reader.Close()
	if closeErr != nil {
		return nil, fmt.Errorf("close dataset validation reader: %w", closeErr)
	}
	stat, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat dataset: %w", err)
	}
	return &SeismicDataset{Path: abs, Metadata: Metadata{
		FileSize:            info.FileSize,
		ModifiedAt:          stat.ModTime(),
		Endian:              info.Endian,
		SampleIntervalUS:    info.SampleIntervalUS,
		SamplesPerTrace:     info.SamplesPerTrace,
		FormatCode:          info.FormatCode,
		BytesPerSample:      info.BytesPerSample,
		ExtendedTextHeaders: info.ExtendedTextHeaders,
		DataStart:           info.DataStart,
		TraceBytes:          info.TraceBytes,
		TraceCount:          info.TraceCount,
	}}, nil
}
