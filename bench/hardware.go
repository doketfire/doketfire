package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// cpuModel and totalRAM are best-effort: the benchmark must still run
// when the OS does not expose the information through the commands used
// here. They are reported, never used for correctness.
func cpuModel() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			"(Get-CimInstance Win32_Processor).Name").Output()
		if err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v
			}
		}
	case "linux":
		data, err := os.ReadFile("/proc/cpuinfo")
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "model name") {
					if _, v, ok := strings.Cut(line, ":"); ok {
						return strings.TrimSpace(v)
					}
				}
			}
		}
	case "darwin":
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string").Output()
		if err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v
			}
		}
	}
	return "unknown"
}

func totalRAM() uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			"(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory").Output()
		if err == nil {
			if n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return n
			}
		}
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
							return kb * 1024
						}
					}
				}
			}
		}
	case "darwin":
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			if n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// processRSS returns the resident set size of the current process. It is
// sampled once per engine, after the workloads and before Close.
func processRSS() uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		out, err := exec.CommandContext(ctx, "tasklist",
			"/FI", fmt.Sprintf("PID eq %d", os.Getpid()),
			"/FO", "CSV", "/NH",
		).Output()
		if err != nil {
			return 0
		}
		line := strings.TrimSpace(string(out))
		parts := strings.Split(line, `","`)
		if len(parts) < 2 {
			return 0
		}
		mem := strings.Trim(parts[len(parts)-1], `" `)
		fields := strings.Fields(mem)
		if len(fields) == 0 {
			return 0
		}
		// tasklist reports "63.972 KB" or "12,345 K" depending on locale.
		num := strings.ReplaceAll(fields[0], ",", "")
		num = strings.ReplaceAll(num, ".", "")
		kb, err := strconv.ParseUint(num, 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	case "linux":
		data, err := os.ReadFile("/proc/self/status")
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						return kb * 1024
					}
				}
			}
		}
	case "darwin":
		out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
		if err == nil {
			if kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}
