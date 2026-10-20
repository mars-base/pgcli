package inspect

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// humanInt renders a count compactly (4500 → "4.5k"), pgbot's style.
func humanInt(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return trimFloat(float64(n)/1e3) + "k"
	case n < 1_000_000_000:
		return trimFloat(float64(n)/1e6) + "M"
	default:
		return trimFloat(float64(n)/1e9) + "B"
	}
}

// humanBytes renders a size in decimal units (18.4 GB), matching the way disk
// and relation sizes are conventionally quoted.
func humanBytes(b int64) string {
	f := float64(b)
	switch {
	case f < 1000:
		return fmt.Sprintf("%d B", b)
	case f < 1e6:
		return trimFloat(f/1e3) + " KB"
	case f < 1e9:
		return trimFloat(f/1e6) + " MB"
	case f < 1e12:
		return trimFloat(f/1e9) + " GB"
	default:
		return trimFloat(f/1e12) + " TB"
	}
}

// trimFloat keeps one decimal and drops a trailing ".0".
func trimFloat(f float64) string {
	s := strconv1f(f)
	return strings.TrimSuffix(s, ".0")
}

func strconv1f(f float64) string { return fmt.Sprintf("%.1f", f) }

// humanMS renders a duration in milliseconds compactly.
func humanMS(ms float64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%.2f ms", ms)
	case ms < 60_000:
		return trimFloat(ms/1000) + "s"
	default:
		return trimFloat(ms/60_000) + "m"
	}
}

func humanSec(s float64) string {
	switch {
	case s < 90:
		return fmt.Sprintf("%.0fs", s)
	case s < 5400:
		return trimFloat(s/60) + "m"
	case s < 86400:
		return trimFloat(s/3600) + "h"
	default:
		return trimFloat(s/86400) + "d"
	}
}

func pct1(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

// sizeScore maps bytes onto 0..100 on a log scale (pgbot's T9 idea): ~1 MiB
// scores near 0, ~100 GiB near 100 — storage pain is logarithmic to humans.
func sizeScore(b int64) float64 {
	if b <= 1<<20 {
		return 0
	}
	s := 100.0 / 17.0 * math.Log2(float64(b)/(1<<20))
	return clamp(s, 0, 100)
}

func clamp(f, lo, hi float64) float64 {
	return math.Min(hi, math.Max(lo, f))
}

// shortDur renders a duration for evidence lines (2.1s, 4m, 6h20m style).
func shortDur(d time.Duration) string {
	s := d.Seconds()
	switch {
	case s < 60:
		return fmt.Sprintf("%.0fs", s)
	case s < 3600:
		return fmt.Sprintf("%.0fm", s/60)
	default:
		h := int(s) / 3600
		m := (int(s) % 3600) / 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
}
