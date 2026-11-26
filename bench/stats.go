package main

import (
	"sort"
	"time"
)

// Stats summarizes one workload. Durations are kept in milliseconds
// because every operation is expected to be far below one second.
type Stats struct {
	Name      string  `json:"name"`
	N         int     `json:"n"`
	P50MS     float64 `json:"p50_ms"`
	P95MS     float64 `json:"p95_ms"`
	P99MS     float64 `json:"p99_ms"`
	MeanMS    float64 `json:"mean_ms"`
	OpsPerSec float64 `json:"ops_per_sec"`
}

func computeStats(name string, samples []time.Duration) Stats {
	if len(samples) == 0 {
		return Stats{Name: name}
	}

	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var total time.Duration
	for _, d := range sorted {
		total += d
	}

	mean := total / time.Duration(len(sorted))
	return Stats{
		Name:      name,
		N:         len(sorted),
		P50MS:     ms(percentile(sorted, 50)),
		P95MS:     ms(percentile(sorted, 95)),
		P99MS:     ms(percentile(sorted, 99)),
		MeanMS:    ms(mean),
		OpsPerSec: opsPerSec(mean),
	}
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p*len(sorted) + 99) / 100 // ceil(p/100*n)
	if idx < 1 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return sorted[idx-1]
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func opsPerSec(mean time.Duration) float64 {
	if mean <= 0 {
		return 0
	}
	return float64(time.Second) / float64(mean)
}
