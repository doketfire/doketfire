package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

type Env struct {
	GeneratedAt   string `json:"generated_at"`
	GoVersion     string `json:"go_version"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	NumCPU        int    `json:"num_cpu"`
	CPUModel      string `json:"cpu_model"`
	TotalRAMBytes uint64 `json:"total_ram_bytes"`
}

type Report struct {
	Environment  Env                  `json:"environment"`
	Config       Config               `json:"config"`
	Results      []EngineResult       `json:"results"`
	BinarySizes  []BinarySize         `json:"binary_sizes,omitempty"`
	CrossCompile []CrossCompileResult `json:"cross_compile,omitempty"`
}

func collectEnv() Env {
	return Env{
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		CPUModel:      cpuModel(),
		TotalRAMBytes: totalRAM(),
	}
}

func (r Report) Markdown() string {
	var b strings.Builder

	b.WriteString("# Gate 0 benchmark — Bbolt vs modernc SQLite\n\n")
	fmt.Fprintf(&b, "> Generado el %s por `bench` (Doketfire).\n\n", r.Environment.GeneratedAt)

	b.WriteString("## Entorno\n\n")
	b.WriteString("| Campo | Valor |\n|---|---|\n")
	fmt.Fprintf(&b, "| Go | `%s` |\n", r.Environment.GoVersion)
	fmt.Fprintf(&b, "| OS/arch | `%s/%s` |\n", r.Environment.GOOS, r.Environment.GOARCH)
	fmt.Fprintf(&b, "| CPU | %s (%d hilos) |\n", r.Environment.CPUModel, r.Environment.NumCPU)
	fmt.Fprintf(&b, "| RAM total | %s |\n", formatBytes(r.Environment.TotalRAMBytes))
	b.WriteString("\n")

	b.WriteString("## Dataset y configuración\n\n")
	b.WriteString("| Parámetro | Valor |\n|---|---:|\n")
	fmt.Fprintf(&b, "| Registros | %d |\n", r.Config.Records)
	fmt.Fprintf(&b, "| Seed determinista | %d |\n", r.Config.Seed)
	fmt.Fprintf(&b, "| Batch de escritura | %d |\n", r.Config.Batch)
	fmt.Fprintf(&b, "| Point reads | %d |\n", r.Config.PointReads)
	fmt.Fprintf(&b, "| Listas | %d |\n", r.Config.Lists)
	fmt.Fprintf(&b, "| Counts | %d |\n", r.Config.Counts)
	fmt.Fprintf(&b, "| Updates | %d |\n", r.Config.Updates)
	fmt.Fprintf(&b, "| Warmup por workload | %d |\n", r.Config.Warmup)
	b.WriteString("\n")

	b.WriteString("## Resultados por motor\n\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "### %s\n\n", res.Engine)
		fmt.Fprintf(&b, "- Apertura: %s\n", formatMS(res.OpenMS))
		fmt.Fprintf(&b, "- Seed: %d filas en %s (%s filas/s)\n\n",
			res.Records, formatMS(res.SeedMS), formatRate(res.SeedRowsPerSec))
		b.WriteString("| Workload | n | p50 | p95 | p99 | ops/s |\n")
		b.WriteString("|---|---:|---:|---:|---:|---:|\n")
		for _, s := range res.Workloads {
			fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s |\n",
				s.Name, s.N, formatMS(s.P50MS), formatMS(s.P95MS), formatMS(s.P99MS), formatRate(s.OpsPerSec))
		}
		b.WriteString("\n")
	}

	if cmp := r.comparison(); cmp != "" {
		b.WriteString("## Comparación directa\n\n")
		b.WriteString("Δ = variación porcentual del motor secundario frente al primero. En latencia, negativo es mejor; en filas/s, positivo es mejor.\n\n")
		b.WriteString(cmp)
		b.WriteString("\n")
	}

	if reading := r.reading(); reading != "" {
		b.WriteString(reading)
		b.WriteString("\n")
	}

	b.WriteString("## Recursos\n\n")
	b.WriteString("| Motor | HeapAlloc | HeapSys | Sys | RSS | DB en disco |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
			res.Engine,
			formatBytes(res.HeapAllocBytes),
			formatBytes(res.HeapSysBytes),
			formatBytes(res.SysBytes),
			formatBytes(res.RSSBytes),
			formatBytes(uint64(res.DiskBytes)),
		)
	}
	b.WriteString("\n")

	if len(r.BinarySizes) > 0 {
		b.WriteString("## Tamaño de binario (dependencia aislada)\n\n")
		b.WriteString("Programas mínimos que solo importan el motor, compilados con `CGO_ENABLED=0` y `-trimpath`.\n\n")
		b.WriteString("| Build | Tamaño |\n|---|---:|\n")
		for _, s := range r.BinarySizes {
			fmt.Fprintf(&b, "| %s | %s |\n", s.Name, formatBytes(uint64(s.Bytes)))
		}
		b.WriteString("\n")
	}

	if len(r.CrossCompile) > 0 {
		b.WriteString("## Cross-compile (`CGO_ENABLED=0`)\n\n")
		b.WriteString("| Target | Resultado | Duración | Binario |\n|---|---|---:|---:|\n")
		for _, c := range r.CrossCompile {
			status := "✅"
			if !c.OK {
				status = "❌ " + c.Error
			}
			fmt.Fprintf(&b, "| %s/%s | %s | %s | %s |\n",
				c.GOOS, c.GOARCH, status, formatMS(c.DurationMS), formatBytes(uint64(c.Bytes)))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Entradas para el go/no-go\n\n")
	b.WriteString("Criterio de abandono de Gate 0: p95 de listas indexadas con 100k filas < 50 ms.\n\n")
	b.WriteString("| Motor | p95 `list_published` | Cumple |\n|---|---:|---|\n")
	for _, res := range r.Results {
		for _, s := range res.Workloads {
			if s.Name != "list_published" {
				continue
			}
			ok := "❌"
			if s.P95MS < 50 {
				ok = "✅"
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", res.Engine, formatMS(s.P95MS), ok)
		}
	}
	b.WriteString("\n")

	b.WriteString("## Notas metodológicas\n\n")
	b.WriteString("- Cada motor se ejecuta en un proceso hijo separado para que la memoria de uno no contamine al otro.\n")
	b.WriteString("- Bbolt guarda documentos JSON y mantiene índices secundarios; SQLite usa columnas, `STRICT`, WAL y `synchronous=NORMAL`.\n")
	b.WriteString("- Bbolt confirma una transacción por lote; SQLite usa `_txlock=immediate` y una transacción por lote.\n")
	b.WriteString("- El tamaño en disco de SQLite incluye `-wal` y `-shm` en el momento de la medición; Bbolt solo el archivo `.db`.\n")
	b.WriteString("- No se midió CGO SQLite: requiere un toolchain C/C++ que este entorno no tiene. Queda como referencia pendiente para Gate 0.\n")
	b.WriteString("- Una sola ejecución por motor: la varianza entre runs puede ser alta. Para cerrar Gate 0, repetir al menos 3 veces y usar la mediana.\n")
	b.WriteString("- Las duraciones de cross-compile dependen del estado de la caché de Go; no son un benchmark de compilación en frío.\n")
	b.WriteString("- Los números dependen del hardware, del sistema de archivos y de la caché; se publican para comparar motores, no como promesa absoluta.\n")

	return b.String()
}

func (r Report) comparison() string {
	if len(r.Results) != 2 {
		return ""
	}
	base, cand := r.Results[0], r.Results[1]

	baseStats := statsByName(base.Workloads)
	candStats := statsByName(cand.Workloads)

	var b strings.Builder
	fmt.Fprintf(&b, "| Workload | %s p50 | %s p50 | Δ p50 | %s p95 | %s p95 | Δ p95 |\n",
		base.Engine, cand.Engine, base.Engine, cand.Engine)
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, name := range workloadOrder {
		bs, ok := baseStats[name]
		if !ok {
			continue
		}
		cs, ok := candStats[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n",
			name,
			formatMS(bs.P50MS), formatMS(cs.P50MS), formatDelta(bs.P50MS, cs.P50MS),
			formatMS(bs.P95MS), formatMS(cs.P95MS), formatDelta(bs.P95MS, cs.P95MS),
		)
	}
	fmt.Fprintf(&b, "| seed (filas/s) | %s | %s | %s | — | — | — |\n",
		formatRate(base.SeedRowsPerSec), formatRate(cand.SeedRowsPerSec),
		formatDelta(base.SeedRowsPerSec, cand.SeedRowsPerSec),
	)
	return b.String()
}

func (r Report) reading() string {
	if len(r.Results) != 2 {
		return ""
	}
	base, cand := r.Results[0], r.Results[1]
	bs := statsByName(base.Workloads)
	cs := statsByName(cand.Workloads)

	var b strings.Builder
	b.WriteString("## Lectura rápida\n\n")

	labels := map[string]string{
		"point_get":       "Point reads",
		"list_published":  "Listas indexadas",
		"count_published": "Counts",
		"update_title":    "Updates",
	}
	for _, name := range workloadOrder {
		bsx, ok := bs[name]
		if !ok {
			continue
		}
		csx, ok := cs[name]
		if !ok {
			continue
		}
		winner, fast, slow := base.Engine, bsx.P95MS, csx.P95MS
		if csx.P95MS < bsx.P95MS {
			winner, fast, slow = cand.Engine, csx.P95MS, bsx.P95MS
		}
		ratio := 1.0
		if fast > 0 {
			ratio = slow / fast
		}
		fmt.Fprintf(&b, "- **%s** (p95): gana **%s**, %.1f× más rápido (%s vs %s).\n",
			labels[name], winner, ratio, formatMS(fast), formatMS(slow))
	}

	if base.SeedRowsPerSec > 0 && cand.SeedRowsPerSec > 0 {
		winner, fast, slow := base.Engine, base.SeedRowsPerSec, cand.SeedRowsPerSec
		if cand.SeedRowsPerSec > base.SeedRowsPerSec {
			winner, fast, slow = cand.Engine, cand.SeedRowsPerSec, base.SeedRowsPerSec
		}
		fmt.Fprintf(&b, "- **Seed**: gana **%s**, %.1f× más rápido (%.0f vs %.0f filas/s).\n",
			winner, fast/slow, fast, slow)
	}

	if base.DiskBytes > 0 && cand.DiskBytes > 0 {
		winner, small, big := base.Engine, base.DiskBytes, cand.DiskBytes
		if cand.DiskBytes < base.DiskBytes {
			winner, small, big = cand.Engine, cand.DiskBytes, base.DiskBytes
		}
		fmt.Fprintf(&b, "- **Disco**: gana **%s**, %.1f× menos espacio (%s vs %s).\n",
			winner, float64(big)/float64(small), formatBytes(uint64(small)), formatBytes(uint64(big)))
	}

	if len(r.BinarySizes) == 2 {
		var bboltSize, sqliteSize int64
		for _, s := range r.BinarySizes {
			switch s.Name {
			case "bbolt-only":
				bboltSize = s.Bytes
			case "sqlite-only":
				sqliteSize = s.Bytes
			}
		}
		if bboltSize > 0 && sqliteSize > bboltSize {
			fmt.Fprintf(&b, "- **Binario**: el build mínimo con SQLite añade %s frente a Bbolt (%s vs %s).\n",
				formatBytes(uint64(sqliteSize-bboltSize)),
				formatBytes(uint64(sqliteSize)), formatBytes(uint64(bboltSize)))
		}
	}

	if s, ok := cs["list_published"]; ok {
		verdict := "✅ cumple"
		if s.P95MS >= 50 {
			verdict = "❌ no cumple"
		}
		fmt.Fprintf(&b, "- **Criterio Gate 0**: p95 de `list_published` con %d filas = %s (< 50 ms) → %s.\n",
			r.Config.Records, formatMS(s.P95MS), verdict)
		if r.Config.Records != 100000 {
			b.WriteString("  - Nota: el criterio oficial se define con 100k filas; este run usa otro tamaño de dataset.\n")
		}
	}

	return b.String()
}

var workloadOrder = []string{"point_get", "list_published", "count_published", "update_title"}

func statsByName(stats []Stats) map[string]Stats {
	out := make(map[string]Stats, len(stats))
	for _, s := range stats {
		out[s.Name] = s
	}
	return out
}

func formatMS(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("%.1f ms", v)
	case v >= 10:
		return fmt.Sprintf("%.2f ms", v)
	default:
		return fmt.Sprintf("%.3f ms", v)
	}
}

func formatRate(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func formatDelta(base, cand float64) string {
	if base == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.1f%%", (cand-base)/base*100)
}

func formatBytes(v uint64) string {
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := uint64(unit), 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
}
