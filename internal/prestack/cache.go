package prestack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const DefaultCacheMaxFileSize int64 = 1 << 30

// CacheOptions affects only the metadata cache, never SEG-Y or display data.
// A zero limit uses 1 GiB. Larger limits are capped at that safety ceiling.
type CacheOptions struct {
	Directory   string
	Bypass      bool
	MaxFileSize int64
}

type IndexBuildStats struct {
	Hit              bool
	MissReason       string
	CachePath        string
	ScannedHeaders   int64
	FingerprintReads int64 // header reads in completed successful identity passes
	Duration         time.Duration
	WriteWarning     string
}

// BuildIndexCached retains caller ownership of reader. The cache contains only
// decoded base-header records, before derived geometry modifies their midpoint.
// Both paths rebuild a fresh index through the same code. It is an optimization,
// not a source integrity guarantee: the fingerprint samples at most 64 headers
// per identity check, and never reads amplitudes. Bypass forces a complete scan.
func BuildIndexCached(ctx context.Context, reader *segy.File, mapping HeaderMapping, options CacheOptions, progress func(IndexProgress)) (index *PrestackIndex, stats IndexBuildStats, err error) {
	started := time.Now()
	defer func() { stats.Duration = time.Since(started) }()
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return nil, stats, err
	}
	if reader == nil {
		return nil, stats, errors.New("nil SEG-Y reader")
	}
	if err = mapping.Validate(); err != nil {
		return nil, stats, err
	}
	n := reader.Info.TraceCount
	if n < 1 || int64(int(n)) != n {
		return nil, stats, errors.New("invalid trace count")
	}
	emit := func(stage string) {
		if progress != nil {
			progress(IndexProgress{Total: n, Scanned: stats.ScannedHeaders, Stage: stage})
		}
	}
	emit("检查道头缓存")
	identity, err := reader.MetadataIdentity(ctx)
	if err != nil {
		return nil, stats, err
	}
	stats.FingerprintReads += int64(identity.HeaderReads)
	identityBytes := encodeCacheIdentity(identity, mapping)
	key := sha256.Sum256(identityBytes)
	limit := options.MaxFileSize
	if limit <= 0 || limit > DefaultCacheMaxFileSize {
		limit = DefaultCacheMaxFileSize
	}
	bypass := options.Bypass || os.Getenv("SEISFORGE_PRESTACK_CACHE_BYPASS") == "1"
	cacheEnabled := !bypass
	directory := options.Directory
	if cacheEnabled && directory == "" {
		var root string
		root, err = os.UserCacheDir()
		if err != nil {
			cacheEnabled = false
			stats.WriteWarning = "user cache directory: " + err.Error()
		} else {
			directory = filepath.Join(root, "Limage", "prestack_metadata", "v1")
		}
	}
	if cacheEnabled {
		stats.CachePath = filepath.Join(directory, "sfmeta-v1-"+hex.EncodeToString(key[:])+".pidx")
		index, err = readMetadataCache(ctx, stats.CachePath, identityBytes, reader.Info, mapping, limit)
		if err == nil {
			stats.Hit = true
			emit("缓存命中")
		} else {
			if ctx.Err() != nil {
				return nil, stats, ctx.Err()
			}
			stats.MissReason = err.Error()
			index = nil
		}
	} else if bypass {
		stats.MissReason = "cache bypass requested"
	} else {
		stats.MissReason = "cache directory unavailable"
	}
	var pending *pendingMetadataCache
	defer func() {
		if pending != nil {
			pending.discard()
		}
	}()
	if !stats.Hit {
		stage := "缓存失效，扫描道头"
		if bypass {
			stage = "绕过缓存，扫描道头"
		}
		emit(stage)
		index, err = scanIndex(ctx, reader, mapping, func(p IndexProgress) {
			stats.ScannedHeaders = p.Scanned
			p.Stage = stage
			if progress != nil {
				progress(p)
			}
		})
		if err != nil {
			return nil, stats, err
		}
		stats.ScannedHeaders = n
		if cacheEnabled {
			// Save raw decode records before rebuildIndex replaces fallback CDP
			// XY with derived midpoints. No second full record array is needed.
			pending, err = prepareMetadataCache(ctx, stats.CachePath, identityBytes, index.Records, limit)
			if err != nil {
				if ctx.Err() != nil {
					return nil, stats, ctx.Err()
				}
				stats.WriteWarning = err.Error()
			}
		}
	}
	if err = rebuildIndex(ctx, index, stats.ScannedHeaders, progress); err != nil {
		return nil, stats, err
	}
	// Invoke the final observer before the publication gate as callbacks can
	// cancel the task (or modify the source in a diagnostic/test). Nothing that
	// can re-enter application code runs after this gate.
	if progress != nil {
		progress(IndexProgress{Done: n, Total: n, Scanned: stats.ScannedHeaders, Stage: "加载完成"})
	}
	// Validate again after both cache I/O and derived reconstruction, including
	// modifications made by a progress callback. Do not publish on any source
	// read/identity error, even if cached records themselves were valid.
	checkSource := func() error {
		after, e := reader.MetadataIdentity(ctx)
		if e != nil {
			return e
		}
		stats.FingerprintReads += int64(after.HeaderReads)
		if identity != after {
			return segy.ErrSourceChanged
		}
		return ctx.Err()
	}
	if err = checkSource(); err != nil {
		return nil, stats, err
	}
	if pending != nil {
		if err = pending.publish(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, stats, ctx.Err()
			}
			stats.WriteWarning = fmt.Sprintf("cache publish: %v", err)
		}
		// Windows may retry a sharing conflict during atomic publication. Do
		// not return a stale in-memory index if the source changed during that
		// interval. A fully published old-identity cache is left intact: it is
		// content-keyed and removing it could remove a concurrent writer's file.
		if err = checkSource(); err != nil {
			return nil, stats, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, stats, err
	}
	return index, stats, nil
}
