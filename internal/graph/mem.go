package graph

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Build memory, measured on kubernetes (ADR 007): peak RSS was 3.9-4.4 GB
// with 1, 4, 8 or 16 workers — nearly all of it the shared importer's decoded
// dependencies plus the graph — so workers are cheap and the base dominates.
const (
	baseBuildBytes = 3 << 30
	perWorkerBytes = 64 << 20
)

// sizing returns the memory limit and worker count for a full build: half
// of the memory available now unless MemBudget is set.
func (s *Service) sizing() (budget int64, workers int) {
	budget = s.MemBudget
	if budget <= 0 {
		budget = availableMemory() / 2
	}
	workers = s.Workers
	if workers <= 0 {
		workers = int((budget - baseBuildBytes) / perWorkerBytes)
	}
	return budget, max(1, min(workers, runtime.GOMAXPROCS(0)))
}

// availableMemory is MemAvailable on Linux; elsewhere a conservative 8 GB.
func availableMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 8 << 30
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fl := strings.Fields(sc.Text()); len(fl) >= 2 && fl[0] == "MemAvailable:" {
			if kb, err := strconv.ParseInt(fl[1], 10, 64); err == nil {
				return kb << 10
			}
		}
	}
	return 8 << 30
}
