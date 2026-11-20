package main

import (
	"os"

	"soksolv/deadzone"
)

// DZ_CHECK_ESCAPE=1 makes every deadzone escape run both the bitset pass and
// the per-box scan and panic on any difference (exp-420).
func init() {
	if os.Getenv("DZ_CHECK_ESCAPE") != "" {
		deadzone.CheckEscape = true
	}
}
