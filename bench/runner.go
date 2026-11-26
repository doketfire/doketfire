package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Config is shared by the parent process and each per-engine child.
type Config struct {
	Records    int           `json:"records"`
	Seed       int64         `json:"seed"`
	Batch      int           `json:"batch"`
	PointReads int           `json:"point_reads"`
	Lists      int           `json:"lists"`
	Counts     int           `json:"counts"`
	Updates    int           `json:"updates"`
	Warmup     int           `json:"warmup"`
	Timeout    time.Duration `json:"-"`
	Dir        string        `json:"-"`
	Keep       bool          `json:"-"`
}

// EngineResult is produced by a child process that runs exactly one
// engine. Running one engine per process keeps memory measurements from
// being polluted by the previous backend.
type EngineResult struct {
	Engine          string  `json:"engine"`
	Records         int     `json:"records"`
	OpenMS          float64 `json:"open_ms"`
	SeedMS          float64 `json:"seed_ms"`
	SeedRowsPerSec  float64 `json:"seed_rows_per_sec"`
	Workloads       []Stats `json:"workloads"`
	HeapAllocBytes  uint64  `json:"heap_alloc_bytes"`
	HeapSysBytes    uint64  `json:"heap_sys_bytes"`
	SysBytes        uint64  `json:"sys_bytes"`
	TotalAllocBytes uint64  `json:"total_alloc_bytes"`
	RSSBytes        uint64  `json:"rss_bytes"`
	DiskBytes       int64   `json:"disk_bytes"`
	NumGC           uint32  `json:"num_gc"`
	GoVersion       string  `json:"go_version"`
	GOOS            string  `json:"goos"`
	GOARCH          string  `json:"goarch"`
	NumCPU          int     `json:"num_cpu"`
}

func newEngine(name string) (Engine, error) {
	switch name {
	case "bbolt":
		return newBoltEngine(), nil
	case "sqlite":
		return newSQLiteEngine(), nil
	default:
		return nil, fmt.Errorf("unknown engine %q (want bbolt or sqlite)", name)
	}
}

func runEngine(ctx context.Context, name string, cfg Config, records []Record) (EngineResult, error) {
	dir := filepath.Join(cfg.Dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return EngineResult{}, err
	}
	if !cfg.Keep {
		defer os.RemoveAll(dir)
	}

	eng, err := newEngine(name)
	if err != nil {
		return EngineResult{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = eng.Close()
		}
	}()

	start := time.Now()
	if err := eng.Open(ctx, dir); err != nil {
		return EngineResult{}, fmt.Errorf("open: %w", err)
	}
	openDur := time.Since(start)

	res := EngineResult{
		Engine:    name,
		Records:   len(records),
		OpenMS:    ms(openDur),
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
	}

	seedStart := time.Now()
	if err := eng.Seed(ctx, records, cfg.Batch); err != nil {
		return EngineResult{}, fmt.Errorf("seed: %w", err)
	}
	seedDur := time.Since(seedStart)
	res.SeedMS = ms(seedDur)
	if seedDur > 0 {
		res.SeedRowsPerSec = float64(len(records)) / seedDur.Seconds()
	}

	rng := rand.New(rand.NewSource(cfg.Seed + 1))
	if err := warmup(ctx, eng, records, rng, cfg.Warmup); err != nil {
		return EngineResult{}, fmt.Errorf("warmup: %w", err)
	}

	pointSamples := make([]time.Duration, 0, cfg.PointReads)
	for i := 0; i < cfg.PointReads; i++ {
		id := records[rng.Intn(len(records))].ID
		t0 := clockNow()
		rec, err := eng.PointGet(ctx, id)
		pointSamples = append(pointSamples, time.Duration(clockNow()-t0))
		if err != nil {
			return EngineResult{}, fmt.Errorf("point get: %w", err)
		}
		if rec.ID != id {
			return EngineResult{}, fmt.Errorf("point get returned %q, want %q", rec.ID, id)
		}
	}

	listSamples := make([]time.Duration, 0, cfg.Lists)
	for i := 0; i < cfg.Lists; i++ {
		before := listCursor(records, rng)
		t0 := clockNow()
		rows, err := eng.ListPublished(ctx, 20, before)
		listSamples = append(listSamples, time.Duration(clockNow()-t0))
		if err != nil {
			return EngineResult{}, fmt.Errorf("list published: %w", err)
		}
		if len(rows) > 20 {
			return EngineResult{}, fmt.Errorf("list published returned %d rows, want <= 20", len(rows))
		}
		for j := 1; j < len(rows); j++ {
			if rows[j].CreatedAt > rows[j-1].CreatedAt {
				return EngineResult{}, fmt.Errorf("list published is not sorted by created_at DESC")
			}
		}
	}

	countSamples := make([]time.Duration, 0, cfg.Counts)
	for i := 0; i < cfg.Counts; i++ {
		t0 := clockNow()
		n, err := eng.CountPublished(ctx)
		countSamples = append(countSamples, time.Duration(clockNow()-t0))
		if err != nil {
			return EngineResult{}, fmt.Errorf("count published: %w", err)
		}
		if n <= 0 {
			return EngineResult{}, fmt.Errorf("count published returned %d", n)
		}
	}

	updateSamples := make([]time.Duration, 0, cfg.Updates)
	for i := 0; i < cfg.Updates; i++ {
		id := records[rng.Intn(len(records))].ID
		t0 := clockNow()
		err := eng.UpdateTitle(ctx, id, fmt.Sprintf("updated-%d-%d", i, rng.Int63()))
		updateSamples = append(updateSamples, time.Duration(clockNow()-t0))
		if err != nil {
			return EngineResult{}, fmt.Errorf("update title: %w", err)
		}
	}

	res.Workloads = []Stats{
		computeStats("point_get", pointSamples),
		computeStats("list_published", listSamples),
		computeStats("count_published", countSamples),
		computeStats("update_title", updateSamples),
	}

	disk, err := eng.DiskBytes()
	if err != nil {
		return EngineResult{}, fmt.Errorf("disk bytes: %w", err)
	}
	res.DiskBytes = disk

	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	res.HeapAllocBytes = m.HeapAlloc
	res.HeapSysBytes = m.HeapSys
	res.SysBytes = m.Sys
	res.TotalAllocBytes = m.TotalAlloc
	res.NumGC = m.NumGC
	res.RSSBytes = processRSS()

	if err := eng.Close(); err != nil {
		return EngineResult{}, fmt.Errorf("close: %w", err)
	}
	closed = true
	return res, nil
}

func warmup(ctx context.Context, eng Engine, records []Record, rng *rand.Rand, n int) error {
	for i := 0; i < n; i++ {
		id := records[rng.Intn(len(records))].ID
		if _, err := eng.PointGet(ctx, id); err != nil {
			return err
		}
		if _, err := eng.ListPublished(ctx, 20, listCursor(records, rng)); err != nil {
			return err
		}
		if _, err := eng.CountPublished(ctx); err != nil {
			return err
		}
		if err := eng.UpdateTitle(ctx, id, fmt.Sprintf("warmup-%d", i)); err != nil {
			return err
		}
	}
	return nil
}

func listCursor(records []Record, rng *rand.Rand) int64 {
	if len(records) == 0 {
		return math.MaxInt64
	}
	// Use the upper half of the dataset so every list has rows to return.
	start := len(records) / 2
	idx := start + rng.Intn(len(records)-start)
	return records[idx].CreatedAt + 1
}
