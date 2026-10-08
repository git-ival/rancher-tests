package k3k

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Progress reports lifecycle activity so that long waits are visible.
//
// Several steps legitimately take minutes: a k3k cluster becoming ready, a LoadBalancer
// address being allocated, and above all cattle-cluster-agent connecting back to Rancher after
// an import. Without reporting, those look indistinguishable from a hang.
//
// It is a package-level hook with a no-op default so that tests stay quiet; the command wires
// it to stderr in main.
var Progress = func(format string, args ...any) {}

var progressMu sync.Mutex

// SetProgressWriter sends progress lines to w, prefixed with a timestamp.
func SetProgressWriter(w io.Writer) {
	Progress = func(format string, args ...any) {
		progressMu.Lock()
		defer progressMu.Unlock()
		fmt.Fprintf(w, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
}

// progressInterval is how often a long-running wait restates what it is waiting for.
const progressInterval = 30 * time.Second
