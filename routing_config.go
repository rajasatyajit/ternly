package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// routingConfig is the "routing" config key (ADR 018). It takes a string
// ("v1" or "v2", the escape hatch) or an object:
//
//	"routing": {"version": "v2", "time_value_usd_per_hour": 20,
//	            "background_eval": {"enabled": true, "per_model_minutes": 20, "runs_per_week": 4}}
type routingConfig struct {
	Version        string   `json:"version"`
	TimeValue      *float64 `json:"time_value_usd_per_hour"`
	BackgroundEval evalCaps `json:"background_eval"`
}

// evalCaps bound background evaluations of new models (ADR 018 review,
// decision 3): Ollama Cloud quota only, per model and per week, with an off
// switch; paid APIs need an explicit budget (default $0).
type evalCaps struct {
	Enabled         *bool    `json:"enabled"`
	PerModelMinutes *float64 `json:"per_model_minutes"`
	RunsPerWeek     *int     `json:"runs_per_week"`
	PaidUSD         float64  `json:"paid_usd_per_week"`
}

func (r *routingConfig) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		r.Version = s
		return nil
	}
	type plain routingConfig
	return json.Unmarshal(b, (*plain)(r))
}

// modelsCtx is the context size /models ranks for: a typical agent context
// (ADR 018: 10–60 k tokens).
const modelsCtx = 20000

// defaultRouting is the router a config without "routing" gets.
const defaultRouting = "v1"

// costModel builds routing v2's cost model, or nil for v1. flag overrides
// the config's version.
func (r routingConfig) costModel(flag, dataDir string, turn time.Duration) (*discover.CostModel, error) {
	v := r.Version
	if flag != "" {
		v = flag
	}
	if v == "" {
		v = defaultRouting
	}
	switch v {
	case "v1":
		return nil, nil
	case "v2":
	default:
		return nil, fmt.Errorf("routing: %q (want v1 or v2)", v)
	}
	lambda := discover.DefaultTimeValue
	if r.TimeValue != nil {
		if *r.TimeValue < 0 {
			return nil, fmt.Errorf("routing.time_value_usd_per_hour: %v (want ≥ 0; 0 routes on price alone)", *r.TimeValue)
		}
		lambda = *r.TimeValue
	}
	return &discover.CostModel{TimeValue: lambda, TurnLimit: turn, Speeds: discover.OpenSpeeds(filepath.Join(dataDir, "speed.json"))}, nil
}

// watchdogSetting is "reasoning_watchdog" (issue #2): {"tokens": N,
// "seconds": S}, either of which trips it (0: default, -1: off). The v0.1
// form, a bare number of stream chunks, is read as tokens for a release,
// with a note: Ollama sends about one token per chunk.
type watchdogSetting struct {
	Tokens  int     `json:"tokens"`
	Seconds float64 `json:"seconds"`
	legacy  bool
}

func (w *watchdogSetting) UnmarshalJSON(b []byte) error {
	var n int
	if json.Unmarshal(b, &n) == nil {
		*w = watchdogSetting{Tokens: n, legacy: true}
		return nil
	}
	type plain watchdogSetting
	return json.Unmarshal(b, (*plain)(w))
}
