//go:build !linux && !darwin

package idle

// OnAC: unknown on this platform, counted as AC (ADR 031).
func OnAC() (onAC, known bool) { return true, false }
