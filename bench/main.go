package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfg       Config
		engines   string
		out       string
		jsonOut   string
		child     bool
		engine    string
		childJSON string
		doSizes   bool
		doCross   bool
	)

	flag.IntVar(&cfg.Records, "records", 100000, "number of records in the dataset")
	flag.Int64Var(&cfg.Seed, "seed", 42, "deterministic dataset seed")
	flag.IntVar(&cfg.Batch, "batch", 1000, "rows per write transaction")
	flag.IntVar(&cfg.PointReads, "point-reads", 2000, "point reads per engine")
	flag.IntVar(&cfg.Lists, "lists", 500, "indexed list queries per engine")
	flag.IntVar(&cfg.Counts, "counts", 50, "count queries per engine")
	flag.IntVar(&cfg.Updates, "updates", 500, "updates per engine")
	flag.IntVar(&cfg.Warmup, "warmup", 50, "warmup iterations per workload")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Minute, "overall timeout")
	flag.StringVar(&engines, "engines", "bbolt,sqlite", "comma-separated engines (bbolt, sqlite)")
	flag.StringVar(&out, "out", "", "write the Markdown report to this path")
	flag.StringVar(&jsonOut, "json", "", "write the combined JSON report to this path")
	flag.BoolVar(&cfg.Keep, "keep", false, "keep temporary database files")
	flag.BoolVar(&doSizes, "sizes", true, "measure isolated binary sizes")
	flag.BoolVar(&doCross, "cross", false, "verify cross-compilation for linux/darwin")

	flag.BoolVar(&child, "child", false, "internal: run a single engine")
	flag.StringVar(&engine, "engine", "", "internal: engine name for -child")
	flag.StringVar(&childJSON, "child-json", "", "internal: per-engine JSON output")
	flag.StringVar(&cfg.Dir, "dir", "", "internal: parent directory for engine data")

	flag.Parse()

	if cfg.Records <= 0 {
		return fmt.Errorf("-records must be > 0")
	}
	if cfg.Batch <= 0 {
		return fmt.Errorf("-batch must be > 0")
	}
	if cfg.PointReads < 0 || cfg.Lists < 0 || cfg.Counts < 0 || cfg.Updates < 0 || cfg.Warmup < 0 {
		return fmt.Errorf("workload counts cannot be negative")
	}
	if cfg.Timeout <= 0 {
		return fmt.Errorf("-timeout must be > 0")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	if child {
		return runChild(ctx, engine, cfg, childJSON)
	}

	names := splitList(engines)
	if len(names) == 0 {
		return fmt.Errorf("-engines cannot be empty")
	}

	baseDir, err := os.MkdirTemp("", "doketfire-bench-*")
	if err != nil {
		return err
	}
	if cfg.Keep {
		fmt.Fprintf(os.Stderr, "bench: keeping data in %s\n", baseDir)
	} else {
		defer os.RemoveAll(baseDir)
	}

	results := make([]EngineResult, 0, len(names))
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "bench: running %s (%d records)...\n", name, cfg.Records)

		jsonPath := filepath.Join(baseDir, name+".json")
		args := []string{
			"-child",
			"-engine", name,
			"-records", strconv.Itoa(cfg.Records),
			"-seed", strconv.FormatInt(cfg.Seed, 10),
			"-batch", strconv.Itoa(cfg.Batch),
			"-point-reads", strconv.Itoa(cfg.PointReads),
			"-lists", strconv.Itoa(cfg.Lists),
			"-counts", strconv.Itoa(cfg.Counts),
			"-updates", strconv.Itoa(cfg.Updates),
			"-warmup", strconv.Itoa(cfg.Warmup),
			"-timeout", cfg.Timeout.String(),
			"-dir", baseDir,
			"-child-json", jsonPath,
		}
		if cfg.Keep {
			args = append(args, "-keep")
		}

		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("engine %q: %w", name, err)
		}

		data, err := os.ReadFile(jsonPath)
		if err != nil {
			return fmt.Errorf("engine %q result: %w", name, err)
		}
		var res EngineResult
		if err := json.Unmarshal(data, &res); err != nil {
			return fmt.Errorf("engine %q result: %w", name, err)
		}
		results = append(results, res)
	}

	rep := Report{
		Environment: collectEnv(),
		Config:      cfg,
		Results:     results,
	}

	if doSizes {
		sizes, err := measureBinarySizes(ctx, baseDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bench: warning: binary sizes: %v\n", err)
		} else {
			rep.BinarySizes = sizes
		}
	}

	if doCross {
		fmt.Fprintln(os.Stderr, "bench: verifying cross-compilation...")
		rep.CrossCompile = measureCrossCompile(ctx, baseDir)
	}

	markdown := rep.Markdown()
	fmt.Print(markdown)

	if out != "" {
		if err := os.WriteFile(out, []byte(markdown), 0o644); err != nil {
			return err
		}
	}
	if jsonOut != "" {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(jsonOut, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func runChild(ctx context.Context, engine string, cfg Config, jsonPath string) error {
	if engine == "" {
		return fmt.Errorf("-child requires -engine")
	}
	if cfg.Dir == "" {
		return fmt.Errorf("-child requires -dir")
	}

	records := GenerateDataset(cfg.Records, cfg.Seed)
	res, err := runEngine(ctx, engine, cfg, records)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if jsonPath != "" {
		return os.WriteFile(jsonPath, data, 0o644)
	}
	_, err = os.Stdout.Write(append(data, '\n'))
	return err
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
