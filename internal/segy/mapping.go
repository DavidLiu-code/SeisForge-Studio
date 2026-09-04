package segy

import "sync/atomic"

const maximumMappedReaders = 4

var (
	mappedReaderSemaphore = make(chan struct{}, maximumMappedReaders)
	activeReadMappings    int64
	peakReadMappings      int64
)

type mappedReadRange struct {
	Offset, Length int64
}

func (s *File) ensureReadMapping() ([]byte, bool) {
	if s == nil || s.f == nil || s.Info.FileSize <= 0 {
		return nil, false
	}
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	if len(s.mappingData) > 0 {
		return s.mappingData, true
	}
	if s.mappingTried || s.mappingDisabled {
		return nil, false
	}
	s.mappingTried = true
	mappedReaderSemaphore <- struct{}{}
	data, closeView, err := platformMapReadOnly(s.f, s.Info.FileSize)
	if err != nil || len(data) != int(s.Info.FileSize) {
		if closeView != nil {
			_ = closeView()
		}
		<-mappedReaderSemaphore
		return nil, false
	}
	active := atomic.AddInt64(&activeReadMappings, 1)
	for {
		peak := atomic.LoadInt64(&peakReadMappings)
		if active <= peak || atomic.CompareAndSwapInt64(&peakReadMappings, peak, active) {
			break
		}
	}
	s.mappingData = data
	s.mappingClose = func() error {
		err := closeView()
		atomic.AddInt64(&activeReadMappings, -1)
		<-mappedReaderSemaphore
		return err
	}
	return s.mappingData, true
}
