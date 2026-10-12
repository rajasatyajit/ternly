package idle

// OnAC reports whether the machine runs on AC power; known is false when it
// can't tell (counted as AC: ADR 031).
func OnAC() (onAC, known bool) { return sysfsPower("/sys/class/power_supply") }
