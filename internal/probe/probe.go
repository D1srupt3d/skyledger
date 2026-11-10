// Package probe keeps the files the health checks read (compose.yml, the Helm chart).
// Alive is touched every loop (the loop isn't hung), Ready after every successful write
// or run (data is flowing).
package probe

import (
	"os"
	"time"
)

// Paths are variables so tests can point them elsewhere.
var (
	Alive = "/tmp/alive"
	Ready = "/tmp/ready"
)

// Touch creates path or updates its modification time. Errors are ignored: a probe
// that can't be written shows up as a failing health check, which is the point.
func Touch(path string) {
	now := time.Now()
	if os.Chtimes(path, now, now) == nil {
		return
	}
	if f, err := os.Create(path); err == nil {
		_ = f.Close()
	}
}
