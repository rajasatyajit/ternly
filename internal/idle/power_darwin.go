package idle

import (
	"context"
	"os/exec"
	"time"
)

// OnAC reports whether the machine runs on AC power; known is false when it
// can't tell (counted as AC: ADR 031).
func OnAC() (onAC, known bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "batt").Output()
	if err != nil {
		return true, false
	}
	return pmsetPower(string(out))
}
