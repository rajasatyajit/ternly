package discover

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// Hardware is this machine's room for local models (ADR 018 §3). Zero means
// unknown.
type Hardware struct {
	VRAM     int64  // bytes of GPU memory (largest GPU)
	RAMAvail int64  // bytes of available system memory
	GPU      string // how VRAM was found: "nvidia-smi", "amdgpu" or ""
}

// DetectHardware reads VRAM from nvidia-smi or the amdgpu sysfs files, and
// available RAM from /proc/meminfo. Anything it can't read stays unknown.
func DetectHardware(ctx context.Context) Hardware {
	var hw Hardware
	hw.RAMAvail = memAvailable("/proc/meminfo")
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output(); err == nil {
		if v := nvidiaVRAM(string(out)); v > 0 {
			hw.VRAM, hw.GPU = v, "nvidia-smi"
			return hw
		}
	}
	if v := amdVRAM("/sys/class/drm"); v > 0 {
		hw.VRAM, hw.GPU = v, "amdgpu"
	}
	return hw
}

// nvidiaVRAM parses nvidia-smi's MiB column, one GPU per line; the largest
// GPU counts (Ollama places a model on one GPU unless it has to split).
func nvidiaVRAM(out string) int64 {
	var best int64
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if mib, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64); err == nil {
			best = max(best, mib<<20)
		}
	}
	return best
}

func amdVRAM(drm string) int64 {
	var best int64
	paths, _ := filepath.Glob(filepath.Join(drm, "card*", "device", "mem_info_vram_total"))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			best = max(best, v)
		}
	}
	return best
}

func memAvailable(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemAvailable:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			if err == nil {
				return kb << 10
			}
		}
	}
	return 0
}

// loaded is what Ollama's /api/ps reports for a model in memory.
type loaded struct{ Size, VRAM int64 }

// ollamaPS lists the models Ollama has loaded, with how much of each is on
// the GPU (size_vram of size): the same numbers `ollama ps` shows.
func ollamaPS(ctx context.Context, base string) map[string]loaded {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/ps", nil)
	resp, err := llm.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var d struct {
		Models []struct {
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d) != nil {
		return nil
	}
	out := map[string]loaded{}
	for _, m := range d.Models {
		out[m.Name] = loaded{m.Size, m.SizeVRAM}
	}
	return out
}

// kvOverhead: memory a loaded model needs beyond its weights (KV cache and
// buffers). qwen3.6 here: 23.0 GB on disk, 25.1 GB loaded (ADR 018).
const kvOverhead = 1.15

// Place sets m.GPU, the fraction of a local model on the GPU: measured when
// Ollama has it loaded, else estimated from its size and the VRAM. Cloud and
// remote models get no placement.
func Place(m *Model, hw Hardware, ps map[string]loaded) {
	m.GPU, m.GPUBasis = -1, ""
	if !m.Local() {
		return
	}
	if l, ok := ps[m.ID]; ok && l.Size > 0 {
		m.GPU, m.GPUBasis = min(1, float64(l.VRAM)/float64(l.Size)), "loaded"
		return
	}
	if hw.VRAM > 0 && m.Size > 0 {
		need := float64(m.Size) * kvOverhead
		m.GPU, m.GPUBasis = min(1, 0.9*float64(hw.VRAM)/need), "estimated"
	}
}

// Fits reports whether a local model can be loaded at all: weights plus
// overhead within VRAM + available RAM. Unknown sizes fit.
func Fits(m *Model, hw Hardware) bool {
	if !m.Local() || m.Size == 0 || hw.RAMAvail == 0 {
		return true
	}
	return float64(m.Size)*kvOverhead <= float64(hw.VRAM+hw.RAMAvail)
}
