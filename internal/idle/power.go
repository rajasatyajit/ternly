package idle

import (
	"os"
	"path/filepath"
	"strings"
)

// sysfsPower reads Linux's power supplies under dir
// (/sys/class/power_supply): on AC when a Mains or USB supply is online, or
// when there's no system battery at all (a desktop). Batteries of devices
// (scope "Device": a mouse, a headset) don't count. known is false when dir
// has no supplies to read (containers, some VMs): unknown counts as AC.
func sysfsPower(dir string) (onAC, known bool) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		return true, false
	}
	read := func(sup, f string) string {
		b, _ := os.ReadFile(filepath.Join(dir, sup, f))
		return strings.TrimSpace(string(b))
	}
	battery := false
	for _, e := range ents {
		s := e.Name()
		switch read(s, "type") {
		case "Mains", "USB", "USB_C", "USB_PD":
			if read(s, "online") == "1" {
				return true, true
			}
		case "Battery":
			if read(s, "scope") != "Device" {
				battery = true
			}
		}
	}
	return !battery, true
}

// pmsetPower reads `pmset -g batt` output (macOS): "Now drawing from 'AC
// Power'" or "'Battery Power'" (or 'UPS Power', counted as battery).
func pmsetPower(out string) (onAC, known bool) {
	switch {
	case strings.Contains(out, "'AC Power'"):
		return true, true
	case strings.Contains(out, "'Battery Power'"), strings.Contains(out, "'UPS Power'"):
		return false, true
	}
	return true, false
}
