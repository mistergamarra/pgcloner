// Package progress prints a single-line, byte-count progress indicator to
// stderr while a long-running command (pg_dump, psql restore) runs —
// stdout is left clean for any piped output.
package progress

import (
	"fmt"
	"os"
	"time"

	"github.com/mistergamarra/pgcloner/internal/humanize"
)

var spinner = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// Watch reports progress against a file whose size grows over time (the
// dump/restore output file). Call the returned stop func once the
// underlying command finishes.
func Watch(label string, sizeFn func() int64) (stop func()) {
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
				elapsed := time.Since(start).Round(time.Second)
				var sizeStr string
				if sizeFn != nil {
					sizeStr = "  " + humanize.Bytes(sizeFn())
				}
				fmt.Fprintf(os.Stderr, "\r\033[K%c %s%s  %s",
					spinner[i%len(spinner)], label, sizeStr, elapsed)
				i++
			}
		}
	}()
	return func() { close(done) }
}
