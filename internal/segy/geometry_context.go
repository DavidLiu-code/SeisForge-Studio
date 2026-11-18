package segy

import (
	"context"
	"encoding/gob"
	"io"
	"os"
	"path/filepath"
)

type geometryContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r geometryContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type geometryContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w geometryContextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}
func decodeGeometryCacheContext(ctx context.Context, r io.Reader, cache *geometryCacheFile) error {
	return gob.NewDecoder(geometryContextReader{ctx, r}).Decode(cache)
}

// Cache failures remain non-fatal. Cancellation is an error and prevents both
// file publication and memory-cache insertion. The gob payload/version stays v1.
func saveGeometryCacheContext(ctx context.Context, cp string, idx *GeometryIndex) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if cp == "" || idx == nil {
		return nil
	}
	var published os.FileInfo
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
			// If cancellation raced the final rename, remove only this
			// task's completed file, never a later concurrent replacement.
			if published != nil {
				if current, statErr := os.Stat(cp); statErr == nil && os.SameFile(published, current) {
					_ = os.Remove(cp)
				}
			}
			return
		}
		geometryMemoryCache.Store(cp, idx)
	}()
	f, openErr := os.CreateTemp(filepath.Dir(cp), ".geometry-*.tmp")
	if openErr != nil {
		return nil
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if encErr := gob.NewEncoder(geometryContextWriter{ctx, f}).Encode(geometryCacheFile{Magic: geometryCacheMagic, Version: geometryCacheVersion, Index: *idx}); encErr != nil {
		return ctx.Err()
	}
	if syncErr := f.Sync(); syncErr != nil {
		return ctx.Err()
	}
	completed, _ := f.Stat()
	closeErr := f.Close()
	closed = true
	if closeErr != nil {
		return ctx.Err()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Publish only complete v1 data. Each task owns a unique temporary name,
	// so concurrent builds cannot remove or truncate another task's staging file.
	if os.Rename(tmp, cp) == nil {
		published = completed
	}
	return nil
}
