package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Speed is how fast a model answers on this machine (ADR 018), measured
// passively from every streamed request.
type Speed struct {
	// PrefillTPS: prompt tokens evaluated per second (time to first token over
	// the tokens the server had to evaluate; cached tokens excluded).
	PrefillTPS float64 `json:"prefill_tps,omitempty"`
	// GenTPS: output tokens per second after the first token.
	GenTPS float64 `json:"gen_tps,omitempty"`
	// BaseS: time to first token for a small prompt (network, queueing, load).
	BaseS   float64   `json:"base_s,omitempty"`
	Samples int       `json:"samples"`
	Updated time.Time `json:"updated"`
}

// speedFileVersion is bumped on any incompatible change; a file with another
// version is ignored (speeds are re-measured), never misread.
const speedFileVersion = 1

type speedFile struct {
	Version int               `json:"version"`
	Models  map[string]*Speed `json:"models"`
}

// SpeedStore keeps measured speeds per model key in <data>/speed.json.
type SpeedStore struct {
	mu    sync.Mutex
	path  string
	m     map[string]*Speed
	dirty bool
}

// OpenSpeeds loads the store at path ("" keeps it in memory only).
func OpenSpeeds(path string) *SpeedStore {
	s := &SpeedStore{path: path, m: map[string]*Speed{}}
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var f speedFile
	if json.Unmarshal(b, &f) == nil && f.Version == speedFileVersion && f.Models != nil {
		s.m = f.Models
	}
	return s
}

// Get returns the measured speed of a model, if any.
func (s *SpeedStore) Get(key string) (Speed, bool) {
	if s == nil {
		return Speed{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return Speed{}, false
	}
	return *v, true
}

// ewma weights a new sample: fast to follow a model that moved to the GPU
// (or off it), slow enough that one odd request doesn't swing routing.
const ewma = 0.3

func blend(old, x float64) float64 {
	if old == 0 {
		return x
	}
	return (1-ewma)*old + ewma*x
}

// Observe records one streamed request: evaluated prompt tokens, output
// tokens, time to first token and generation time (first to last token).
// Samples too small to say anything are ignored.
func (s *SpeedStore) Observe(key string, prompt, out int, ttft, gen time.Duration) {
	if s == nil || ttft <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.m[key]
	if v == nil {
		v = &Speed{}
		s.m[key] = v
	}
	switch {
	case prompt >= 1000: // enough prompt that evaluation dominates the wait
		v.PrefillTPS = blend(v.PrefillTPS, float64(prompt)/ttft.Seconds())
	case prompt >= 0:
		v.BaseS = blend(v.BaseS, ttft.Seconds())
	}
	if out >= 32 && gen > 0 {
		v.GenTPS = blend(v.GenTPS, float64(out)/gen.Seconds())
	}
	v.Samples++
	v.Updated = time.Now()
	s.dirty = true
}

// Save writes the store if it changed (0600, atomically).
func (s *SpeedStore) Save() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	b, err := json.MarshalIndent(speedFile{Version: speedFileVersion, Models: s.m}, "", "  ")
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".speed-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
