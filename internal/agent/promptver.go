package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// deterministicSeed is the seed deterministic mode sends (ADR 029).
const deterministicSeed = 20261008

// PromptVersion identifies the prompts this agent sends, independent of the
// run: a hash of the system prompt without its per-run lines (date,
// workspace path, branch) and of every tool's spec. Two runs with the same
// version sent the same instructions; results are recorded with it, so a
// prompt change is visible in every comparison (ADR 029).
func (a *Agent) PromptVersion() string {
	a.mu.Lock()
	sys := a.system
	a.mu.Unlock()
	var keep []string
	for _, l := range strings.Split(sys, "\n") {
		if strings.HasPrefix(l, "Environment: ") || strings.HasPrefix(l, "Git branch: ") || strings.HasPrefix(l, "Project check command: ") {
			continue
		}
		keep = append(keep, l)
	}
	specs, _ := json.Marshal(a.Reg.Specs())
	h := sha256.New()
	h.Write([]byte(strings.Join(keep, "\n")))
	h.Write(specs)
	return hex.EncodeToString(h.Sum(nil))[:12]
}
