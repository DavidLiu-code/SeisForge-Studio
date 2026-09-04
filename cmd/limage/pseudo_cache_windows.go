//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"time"

	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudocachecore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudocache"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// Version 3 adds the canonical world-geometry fingerprint to the persistent
// curtain identity. The raw .cidx geometry cache remains unchanged; only
// derived pseudo-3-D textures are rebuilt.
const pseudoTextureAlgorithmVersion = 3

type pseudoCacheWrite struct {
	key   pseudocachecore.CacheKey
	entry pseudocachecore.Entry
	line  string
}

func pseudoNavigationFingerprint(line *projectcore.CrookedProjectLine) string {
	if line == nil || len(line.Navigation) == 0 {
		return "none"
	}
	hash := sha256.New()
	var raw [8]byte
	putInt := func(value int64) {
		binary.LittleEndian.PutUint64(raw[:], uint64(value))
		_, _ = hash.Write(raw[:])
	}
	putFloat := func(value float64) { putInt(int64(math.Float64bits(value))) }
	putInt(int64(len(line.Navigation)))
	for _, point := range line.Navigation {
		putFloat(point.SP)
		putInt(point.Trace)
		putFloat(point.X)
		putFloat(point.Y)
		putInt(point.MinTrace)
		putInt(point.MaxTrace)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func pseudoPersistentCacheKey(job pseudoLoadJob) (pseudocachecore.CacheKey, bool) {
	if job.canonicalUnavailable || job.line == nil || job.line.Dataset == nil || job.size.Width < 2 || job.size.Height < 2 {
		return pseudocachecore.CacheKey{}, false
	}
	metadata := job.line.Dataset.Metadata
	sampleStart, sampleEnd := job.sampleStart, job.sampleEnd
	if sampleStart < 0 || sampleEnd <= sampleStart || sampleEnd >= metadata.SamplesPerTrace {
		sampleStart, sampleEnd = 0, metadata.SamplesPerTrace-1
	}
	canonicalFingerprint := ""
	if job.geometryCalibrated {
		canonicalFingerprint = job.geometryFingerprint
		if canonicalFingerprint == "" && job.geometry != nil {
			canonicalFingerprint = crookedGeometryFingerprint(job.line.ID, job.geometry)
		}
		// A calibrated job without a canonical geometry identity must never
		// fall back to a legacy cache key.
		if canonicalFingerprint == "" {
			return pseudocachecore.CacheKey{}, false
		}
	}
	return pseudocachecore.CacheKey{
		SourcePath: job.line.Dataset.Path, FileSize: metadata.FileSize, ModTimeUnixNano: metadata.ModifiedAt.UnixNano(),
		DataStart: metadata.DataStart, TraceBytes: metadata.TraceBytes, TraceCount: metadata.TraceCount,
		SamplesPerTrace: metadata.SamplesPerTrace, SampleIntervalUS: metadata.SampleIntervalUS,
		FormatCode: metadata.FormatCode, BytesPerSample: metadata.BytesPerSample, Endian: int(metadata.Endian),
		CoordinateSource: string(job.spec.Source), XByte: job.spec.XByte, YByte: job.spec.YByte, CDPByte: job.spec.CDPByte,
		ScalarByte: job.spec.ScalarByte, UnitsByte: job.spec.UnitsByte,
		NavigationFingerprint: pseudoNavigationFingerprint(job.line), CanonicalGeometryFingerprint: canonicalFingerprint,
		AlgorithmVersion: pseudoTextureAlgorithmVersion,
		Width:            job.size.Width, Height: job.size.Height, GainPercent: job.gain, ClipPercent: job.clip,
		AGC: job.agc, DisplayMode: int(job.displayMode), SampleStart: sampleStart, SampleEnd: sampleEnd,
	}, true
}

func cloneCrookedGeometry(source *geometrycore.CrookedLineGeometry) geometrycore.CrookedLineGeometry {
	if source == nil {
		return geometrycore.CrookedLineGeometry{}
	}
	result := *source
	result.TraceIndices = append([]int64(nil), source.TraceIndices...)
	result.CDP = append([]int32(nil), source.CDP...)
	result.HasCDP = append([]bool(nil), source.HasCDP...)
	result.X = append([]float64(nil), source.X...)
	result.Y = append([]float64(nil), source.Y...)
	result.Distance = append([]float64(nil), source.Distance...)
	return result
}

func pseudoPersistentCacheKeys(job pseudoLoadJob) []pseudocachecore.CacheKey {
	keys := make([]pseudocachecore.CacheKey, 0, 4)
	if key, ok := pseudoPersistentCacheKey(job); ok {
		keys = append(keys, key)
	}
	if job.spec.Source == "" || job.spec.Source == segy.CoordinateAuto || job.spec.XByte == 0 || job.spec.YByte == 0 {
		for _, candidate := range segy.DefaultCoordinateSpecs() {
			candidateJob := job
			candidateJob.spec = candidate
			if key, ok := pseudoPersistentCacheKey(candidateJob); ok {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func pseudoTryPersistentCache(job pseudoLoadJob) (*pseudoLoadResult, bool) {
	if job.textureCache == nil || job.skipPersistentCache {
		return nil, false
	}
	keys := pseudoPersistentCacheKeys(job)
	var entry pseudocachecore.Entry
	var matchedKey pseudocachecore.CacheKey
	hit := false
	for _, key := range keys {
		var loaded bool
		entry, loaded, _ = job.textureCache.Load(key)
		if loaded && entry.Valid() {
			matchedKey = key
			hit = true
			break
		}
	}
	if !hit {
		return nil, false
	}
	if !job.geometryCalibrated && job.projectMultiplier > 0 && math.Abs(entry.Multiplier-job.projectMultiplier) > math.Max(1e-10, math.Abs(job.projectMultiplier)*1e-10) {
		return nil, false
	}
	geometry := cloneCrookedGeometry(&entry.Geometry)
	if matchedKey.CanonicalGeometryFingerprint != "" && crookedGeometryFingerprint(job.line.ID, &geometry) != matchedKey.CanonicalGeometryFingerprint {
		return nil, false
	}
	runs, err := pseudo3dcore.ClipTrajectory(geometry.TraceIndices, geometry.X, geometry.Y, geometry.Distance, job.spatialRange)
	result := &pseudoLoadResult{windowGen: job.windowGen, lineIndex: job.lineIndex, token: job.token,
		rangeGeneration: job.rangeGeneration, timeRangeGeneration: job.timeRangeGeneration, geometry: &geometry, multiplier: entry.Multiplier,
		geometryCalibrated: job.geometryCalibrated, geometryFingerprint: matchedKey.CanonicalGeometryFingerprint,
		calibrationAccepted: entry.CalibrationAccepted, cacheHit: true}
	job.line.Dataset.SetGeometry(&geometry)
	if err != nil {
		result.err = err
		return result, true
	}
	if len(runs) == 0 {
		result.outOfRange = true
		return result, true
	}
	source := pseudoCurtainSegment{
		points: append([]pseudo3dcore.Point(nil), entry.Points...), u: append([]float64(nil), entry.U...),
		texture: &pseudo3dcore.Texture{Width: entry.Width, Height: entry.Height, Indices: entry.Indices},
		length:  entry.Length, positionStart: entry.PositionStart, positionEnd: entry.PositionEnd,
		traceIndices: append([]int64(nil), entry.TraceIndices...), positions: append([]float64(nil), entry.Positions...),
		sampleStart: matchedKey.SampleStart, sampleEnd: matchedKey.SampleEnd, timeStartMS: job.timeStartMS, timeEndMS: job.timeEndMS,
	}
	segments, covered := pseudoSegmentsForRuns(runs, job.size, []pseudoCurtainSegment{source})
	if !covered || len(segments) == 0 {
		return nil, false
	}
	result.segments = segments
	result.points, result.u, result.texture = segments[0].points, segments[0].u, segments[0].texture
	return result, true
}

// pseudoTryPersistentCoveringPreview reuses a complete-time v1 texture only
// as an immediate visual preview. Its amplitude limits belong to the covering
// interval, so the caller must continue with an exact selected-window load.
func pseudoTryPersistentCoveringPreview(job pseudoLoadJob) (*pseudoLoadResult, bool) {
	if job.textureCache == nil || job.skipPersistentCache || job.line == nil || job.line.Dataset == nil {
		return nil, false
	}
	metadata := job.line.Dataset.Metadata
	if job.sampleStart <= 0 && job.sampleEnd >= metadata.SamplesPerTrace-1 {
		return nil, false
	}
	coveringJob := job
	coveringJob.sampleStart, coveringJob.sampleEnd = 0, metadata.SamplesPerTrace-1
	coveringJob.timeStartMS = 0
	coveringJob.timeEndMS = float64(metadata.SamplesPerTrace-1) * float64(metadata.SampleIntervalUS) / 1000
	var entry pseudocachecore.Entry
	var matchedKey pseudocachecore.CacheKey
	hit := false
	for _, key := range pseudoPersistentCacheKeys(coveringJob) {
		var loaded bool
		entry, loaded, _ = job.textureCache.Load(key)
		if loaded && entry.Valid() {
			matchedKey, hit = key, true
			break
		}
	}
	if !hit || (!job.geometryCalibrated && job.projectMultiplier > 0 && math.Abs(entry.Multiplier-job.projectMultiplier) > math.Max(1e-10, math.Abs(job.projectMultiplier)*1e-10)) {
		return nil, false
	}
	geometry := cloneCrookedGeometry(&entry.Geometry)
	if matchedKey.CanonicalGeometryFingerprint != "" && crookedGeometryFingerprint(job.line.ID, &geometry) != matchedKey.CanonicalGeometryFingerprint {
		return nil, false
	}
	runs, err := pseudo3dcore.ClipTrajectory(geometry.TraceIndices, geometry.X, geometry.Y, geometry.Distance, job.spatialRange)
	if err != nil || len(runs) == 0 || matchedKey.SampleEnd <= matchedKey.SampleStart {
		return nil, false
	}
	span := float64(matchedKey.SampleEnd - matchedKey.SampleStart)
	v0 := float64(job.sampleStart-matchedKey.SampleStart) / span
	v1 := float64(job.sampleEnd-matchedKey.SampleStart) / span
	texture := pseudoTextureWindow2D(&pseudo3dcore.Texture{Width: entry.Width, Height: entry.Height, Indices: entry.Indices}, 0, 1, v0, v1, job.size)
	if texture == nil {
		return nil, false
	}
	source := pseudoCurtainSegment{points: append([]pseudo3dcore.Point(nil), entry.Points...), u: append([]float64(nil), entry.U...), texture: texture,
		length: entry.Length, positionStart: entry.PositionStart, positionEnd: entry.PositionEnd,
		traceIndices: append([]int64(nil), entry.TraceIndices...), positions: append([]float64(nil), entry.Positions...),
		sampleStart: job.sampleStart, sampleEnd: job.sampleEnd, timeStartMS: job.timeStartMS, timeEndMS: job.timeEndMS, preview: true}
	segments, _ := pseudoSegmentsForRuns(runs, job.size, []pseudoCurtainSegment{source})
	if len(segments) == 0 {
		return nil, false
	}
	result := &pseudoLoadResult{windowGen: job.windowGen, lineIndex: job.lineIndex, token: job.token, geometry: &geometry,
		rangeGeneration: job.rangeGeneration, timeRangeGeneration: job.timeRangeGeneration, multiplier: entry.Multiplier,
		geometryCalibrated: job.geometryCalibrated, geometryFingerprint: matchedKey.CanonicalGeometryFingerprint,
		calibrationAccepted: entry.CalibrationAccepted, cacheHit: true, previewOnly: true, segments: segments}
	result.points, result.u, result.texture = segments[0].points, segments[0].u, segments[0].texture
	return result, true
}

func pseudoPrepareCacheWrite(job pseudoLoadJob, result *pseudoLoadResult) *pseudoCacheWrite {
	if job.textureCache == nil || job.spatialRange.Valid || result == nil || result.err != nil || result.outOfRange || result.geometry == nil || len(result.segments) != 1 {
		return nil
	}
	segment := result.segments[0]
	if segment.preview || !segment.texture.Valid() {
		return nil
	}
	key, ok := pseudoPersistentCacheKey(job)
	if !ok {
		return nil
	}
	entry := pseudocachecore.Entry{
		Geometry: cloneCrookedGeometry(result.geometry), Points: append([]pseudo3dcore.Point(nil), segment.points...), U: append([]float64(nil), segment.u...),
		TraceIndices: append([]int64(nil), segment.traceIndices...), Positions: append([]float64(nil), segment.positions...),
		PositionStart: segment.positionStart, PositionEnd: segment.positionEnd, Length: segment.length, Multiplier: result.multiplier,
		CalibrationAccepted: result.geometryCalibrated || result.calibrationAccepted || (!job.geometryCalibrated && job.projectMultiplier > 0 && math.Abs(result.multiplier-job.projectMultiplier) <= math.Max(1e-10, math.Abs(job.projectMultiplier)*1e-10)),
		Width:               segment.texture.Width, Height: segment.texture.Height, Indices: segment.texture.Indices,
	}
	if !entry.Valid() {
		return nil
	}
	return &pseudoCacheWrite{key: key, entry: entry, line: job.line.Name}
}

func pseudoRememberCacheWrite(write *pseudoCacheWrite) {
	if write == nil {
		return
	}
	digest, err := write.key.Digest()
	if err != nil || pseudoState.cacheWriteDigests[digest] {
		return
	}
	pseudoState.cacheWriteDigests[digest] = true
	pseudoState.cacheWrites = append(pseudoState.cacheWrites, *write)
}

func pseudoBeginLoadCycle(dropPendingWrites bool) {
	pseudoState.cacheHits, pseudoState.ioReadCalls, pseudoState.ioReadBytes = 0, 0, 0
	pseudoState.inputTraceCount, pseudoState.supportTraceCount = 0, 0
	pseudoState.ioLogicalBytes, pseudoState.ioDecodeNanos = 0, 0
	pseudoState.decodedSampleCount = 0
	pseudoState.mappedSegments, pseudoState.fallbackSegments = 0, 0
	pseudoState.loadStage = int(segy.RenderStageSupportPlan)
	pseudoState.rangePreviewActive, pseudoState.timePreviewActive = false, false
	pseudoState.progressRenderArmed, pseudoState.progressRenderPending = false, false
	pseudoState.progressRenderGen++
	pseudoState.finalRenderPending, pseudoState.finalRenderComplete = false, false
	pseudoState.loadStarted, pseudoState.loadCompletedDuration = time.Now(), 0
	if dropPendingWrites {
		pseudoState.cacheWrites = nil
		pseudoState.cacheWriteDigests = make(map[string]bool)
	}
}

func startPseudoDeferredCacheWrites() {
	if pseudoState.textureCache == nil || pseudoState.cacheWriteQueue == nil || len(pseudoState.cacheWrites) == 0 {
		return
	}
	writes := append([]pseudoCacheWrite(nil), pseudoState.cacheWrites...)
	pseudoState.cacheWrites = nil
	pseudoState.cacheWriteDigests = make(map[string]bool)
	cancel, queue := pseudoState.cancel, pseudoState.cacheWriteQueue
	go func() {
		select {
		case queue <- writes:
		case <-cancel:
		}
	}()
}
