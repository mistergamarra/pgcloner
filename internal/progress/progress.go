// Package progress prints a single-line progress indicator to stderr
// while a long-running command (pg_dump, psql restore) runs — stdout is
// left clean for any piped output.
package progress

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/mistergamarra/pgcloner/internal/humanize"
)

var spinner = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

const (
	maxBarWidth = 24
	minBarWidth = 6
	// fallbackWidth is used when the terminal width can't be detected
	// (not a TTY, redirected output, etc.) — conservative so the line
	// still fits rather than assuming a wide terminal.
	fallbackWidth = 80
)

// Watch reports progress against a file whose size grows over time (the
// dump/restore output file). When totalBytes > 0, it renders a filled
// progress bar and percentage against that estimate; pass 0 when no
// estimate is available (falls back to a plain byte-count/elapsed
// display). Call the returned stop func once the underlying command
// finishes.
//
// The rendered line is sized to the terminal width (detected once, since
// resizing mid-dump is a rare edge case not worth polling for) and
// truncated as a last resort. This matters more than it looks: if the
// line is longer than the terminal, it wraps onto a second row, and `\r`
// only returns to the start of that wrapped continuation — not the true
// start of the line — so redraws overlap garbage instead of cleanly
// overwriting, making the whole thing look frozen until a real newline
// (the next log line) resets it.
func Watch(label string, sizeFn func() int64, totalBytes int64) (stop func()) {
	width := terminalWidth()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		start := time.Now()
		i := 0
		var started bool // true once sizeFn has reported any bytes at all
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
				if cur > 0 {
					started = true
				}
				elapsed := time.Since(start).Round(time.Second)
				spin := spinner[i%len(spinner)]
				var line string
				if totalBytes > 0 && !started {
					// pg_dump (and psql restore) can spend a long time
					// before writing a single byte — resolving the
					// schema/dependency graph, which gets slower with
					// more --exclude-table flags — so a byte-based bar
					// has nothing to show yet. A bar frozen at 0% reads
					// as broken; say what's actually happening instead.
					line = renderPreparing(spin, label, elapsed, width)
				} else {
					line = render(spin, label, cur, totalBytes, elapsed, width)
				}
				fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
				i++
			}
		}
	}()
	return func() { close(done) }
}

// terminalWidth returns stderr's terminal width, or fallbackWidth if it
// can't be determined (redirected/piped output, or any error).
func terminalWidth() int {
	w, _, err := term.GetSize(os.Stderr.Fd())
	if err != nil || w <= 0 {
		return fallbackWidth
	}
	return w
}

// renderPreparing is the indeterminate state shown while no output bytes
// have been observed yet — see the comment in Watch for why that phase
// exists and why a frozen 0% bar would be misleading here.
func renderPreparing(spin rune, label string, elapsed time.Duration, width int) string {
	return truncate(fmt.Sprintf("%c %s  preparing (no data written yet)...  %s",
		spin, label, elapsed), width)
}

func render(spin rune, label string, cur, total int64, elapsed time.Duration, width int) string {
	if total <= 0 {
		var sizeStr string
		if cur > 0 {
			sizeStr = "  " + humanize.Bytes(cur)
		}
		return truncate(fmt.Sprintf("%c %s%s  %s", spin, label, sizeStr, elapsed), width)
	}
	// pg_dump's COPY-format text output rarely matches the source tables'
	// on-disk size exactly (no indexes/TOAST overhead in the dump, but
	// also no binary packing) — this is an estimate, so cur can overshoot
	// total; clamp the bar/percentage instead of showing >100%.
	pct := 100
	if p := cur * 100 / total; p < 100 {
		pct = int(p)
	}

	suffix := fmt.Sprintf(" %3d%%  %s / ~%s  %s", pct, humanize.Bytes(cur), humanize.Bytes(total), elapsed)
	prefix := fmt.Sprintf("%c %s  ", spin, label)
	// Shrink the bar to whatever's left after the parts that can't
	// shrink, rather than building at maxBarWidth and truncating blindly
	// — a half-eaten bar reads better than a half-eaten byte count.
	bw := width - len([]rune(prefix)) - len([]rune(suffix)) - 2 // 2 for "[" "]"
	if bw > maxBarWidth {
		bw = maxBarWidth
	}
	if bw < minBarWidth {
		bw = minBarWidth
	}
	return truncate(prefix+bar(cur, total, bw)+suffix, width)
}

func bar(cur, total int64, width int) string {
	filled := width
	if total > 0 {
		if f := int(int64(width) * cur / total); f < width {
			filled = f
		}
	}
	b := make([]byte, width)
	for i := range b {
		if i < filled {
			b[i] = '#'
		} else {
			b[i] = '-'
		}
	}
	return "[" + string(b) + "]"
}

// truncate is a last-resort safety net for terminals too narrow for even
// the shrunk-bar layout — better to cut the line than let it wrap.
func truncate(s string, width int) string {
	r := []rune(s)
	if width <= 0 || len(r) <= width {
		return s
	}
	return string(r[:width])
}
