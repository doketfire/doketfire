package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type BinarySize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type CrossCompileResult struct {
	GOOS       string  `json:"goos"`
	GOARCH     string  `json:"goarch"`
	OK         bool    `json:"ok"`
	DurationMS float64 `json:"duration_ms"`
	Bytes      int64   `json:"bytes"`
	Error      string  `json:"error,omitempty"`
}

func findModuleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// measureBinarySizes builds two minimal programs that only import the
// storage engine under test. This isolates the dependency cost from the
// benchmark harness itself.
func measureBinarySizes(ctx context.Context, outDir string) ([]BinarySize, error) {
	root := findModuleRoot()
	if root == "" {
		return nil, fmt.Errorf("go.mod not found")
	}

	targets := []struct {
		name string
		pkg  string
	}{
		{"bbolt-only", "./cmd/size-bbolt"},
		{"sqlite-only", "./cmd/size-sqlite"},
	}

	out := make([]BinarySize, 0, len(targets))
	for _, t := range targets {
		exe := filepath.Join(outDir, t.name)
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", exe, t.pkg)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			return out, fmt.Errorf("build %s: %w: %s", t.pkg, err, strings.TrimSpace(string(b)))
		}
		info, err := os.Stat(exe)
		if err != nil {
			return out, err
		}
		out = append(out, BinarySize{Name: t.name, Bytes: info.Size()})
	}
	return out, nil
}

// measureCrossCompile verifies that the benchmark binary (both engines)
// builds for the platforms promised by the product with CGO disabled.
func measureCrossCompile(ctx context.Context, outDir string) []CrossCompileResult {
	root := findModuleRoot()
	if root == "" {
		return nil
	}

	targets := []struct {
		goos   string
		goarch string
	}{
		{"linux", "amd64"},
		{"linux", "arm64"},
		{"darwin", "arm64"},
	}

	results := make([]CrossCompileResult, 0, len(targets))
	for _, t := range targets {
		exe := filepath.Join(outDir, "doketfire-bench-"+t.goos+"-"+t.goarch)
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", exe, ".")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+t.goos, "GOARCH="+t.goarch)

		start := time.Now()
		output, err := cmd.CombinedOutput()
		res := CrossCompileResult{
			GOOS:       t.goos,
			GOARCH:     t.goarch,
			OK:         err == nil,
			DurationMS: ms(time.Since(start)),
		}
		if err != nil {
			res.Error = strings.TrimSpace(string(output))
		} else if info, statErr := os.Stat(exe); statErr == nil {
			res.Bytes = info.Size()
		}
		results = append(results, res)
	}
	return results
}
