package pool

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// The tree occupies two terminal cells; the complete bar occupies 17.
const setupProgressCells = 17

// setupProgress keeps the step transcript on stderr, with an indeterminate bar
// only on terminals. Each operation stops its animation before returning or
// emitting diagnostics. No cursor hiding or terminal mode changes are needed.
type setupProgress struct {
	out      io.Writer
	terminal bool
	width    func() int
	stop     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	visible  bool
	frame    int
}

func newSetupProgress() *setupProgress {
	stderr := os.Stderr
	return &setupProgress{
		out:      stderr,
		terminal: os.Getenv("TERM") != "dumb" && isatty.IsTerminal(stderr.Fd()),
		width:    func() int { return progressTerminalWidth(stderr) },
	}
}

func (p *setupProgress) run(label string, operation func() error) error {
	p.start(label)
	defer p.finish()
	return operation()
}

func (p *setupProgress) start(label string) {
	fmt.Fprintln(p.out, "🌳 "+label)
	// Leave a spare column so neither drawing nor clearing triggers wrapping.
	// Unknown dimensions and narrow terminals retain the static transcript.
	if !p.terminal || p.width() <= setupProgressCells {
		return
	}
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	p.frame = 0
	p.draw()
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.mu.Lock()
				p.draw()
				p.mu.Unlock()
			}
		}
	}()
}

func (p *setupProgress) finish() {
	if p.stop == nil {
		return
	}
	close(p.stop)
	<-p.done
	p.stop = nil
	p.clear()
}

func (p *setupProgress) draw() {
	// Recheck for resizes while an operation is running.
	if p.width() <= setupProgressCells {
		p.clear()
		return
	}
	// Bounce a fixed-width segment, without implying a percentage or ETA.
	position := p.frame % 18
	if position > 9 {
		position = 18 - position
	}
	fmt.Fprintf(p.out, "\r🌳 [%s===%s]", strings.Repeat(" ", position), strings.Repeat(" ", 9-position))
	p.frame++
	p.visible = true
}

func (p *setupProgress) clear() {
	if p.visible {
		// A resize must not make the clearing spaces wrap onto another row.
		if cells := min(setupProgressCells, p.width()-1); cells > 0 {
			fmt.Fprintf(p.out, "\r%s\r", strings.Repeat(" ", cells))
		}
		p.visible = false
	}
}
