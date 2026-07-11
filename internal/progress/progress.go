// Package progress prints a single-line progress indicator to stderr
// while a long-running command (pg_dump, psql restore) runs — stdout is
// left clean for any piped output.
package progress

import (
	"fmt"
	"os"
	"time"

	"github.com/mistergamarra/pgcloner/internal/humanize"
)

var spinner = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

const barWidth = 24

// Watch reports progress against a file whose size grows over time (the
// dump/restore output file). When totalBytes > 0, it renders a filled
// progress bar and percentage against that estimate; pass 0 when no
// estimate is available (falls back to a plain byte-count/elapsed
// display). Call the returned stop func once the underlying command
// finishes.
func Watch(label string, sizeFn func() int64, totalBytes int64) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		start := time.Now()
		i := 0
		for {
			select {
			case <-done:
				fmt.Fprint(os.Stderr, "\r\033[K")
				return
			case <-t.C:
				var cur int64
				if sizeFn != nil {
					cur = sizeFn()
				}
				elapsed := time.Since(start).Round(time.Second)
				spin := spinner[i%len(spinner)]
				fmt.Fprintf(os.Stderr, "\r\033[K%s", render(spin, label, cur, totalBytes, elapsed))
				i++
			}
		}
	}()
	return func() { close(done) }
}

func render(spin rune, label string, cur, total int64, elapsed time.Duration) string {
	if total <= 0 {
		var sizeStr string
		if cur > 0 {
			sizeStr = "  " + humanize.Bytes(cur)
		}
		return fmt.Sprintf("%c %s%s  %s", spin, label, sizeStr, elapsed)
	}
	// pg_dump's COPY-format text output rarely matches the source tables'
	// on-disk size exactly (no indexes/TOAST overhead in the dump, but
	// also no binary packing) — this is an estimate, so cur can overshoot
	// total; clamp the bar/percentage instead of showing >100%.
	pct := 100
	if total > 0 {
		if p := cur * 100 / total; p < 100 {
			pct = int(p)
		}
	}
	return fmt.Sprintf("%c %s  %s %3d%%  %s / ~%s  %s",
		spin, label, bar(cur, total), pct, humanize.Bytes(cur), humanize.Bytes(total), elapsed)
}

func bar(cur, total int64) string {
	filled := barWidth
	if total > 0 {
		if f := int(int64(barWidth) * cur / total); f < barWidth {
			filled = f
		}
	}
	b := make([]byte, barWidth)
	for i := range b {
		if i < filled {
			b[i] = '#'
		} else {
			b[i] = '-'
		}
	}
	return "[" + string(b) + "]"
}
