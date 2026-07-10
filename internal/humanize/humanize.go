// Package humanize formats byte counts consistently across the CLI
// (progress indicators, table-size labels).
package humanize

import "fmt"

// Bytes formats b as a human-readable size. Sizes typical of tables and
// dump files land in KB/MB; B and GB only show up at the extremes.
func Bytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
