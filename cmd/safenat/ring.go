package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ringBuf is a concurrency-safe bounded line buffer that doubles as an
// io.Writer: the process logger writes into it (alongside stderr), and the
// local admin endpoint serves the last lines to `safenat logs`.
type ringBuf struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newRing(max int) *ringBuf {
	return &ringBuf{max: max}
}

// Write appends p to the buffer, splitting on newlines (a slog TextHandler
// emits one line + "\n" per record).
func (r *ringBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(string(p), "\n") {
		if line == "" {
			continue
		}
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	return len(p), nil
}

// Tail returns the last n lines (all when n <= 0).
func (r *ringBuf) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n >= len(r.lines) {
		out := make([]string, len(r.lines))
		copy(out, r.lines)
		return out
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

// sockPath is the local admin socket location for a process kind. It is
// namespaced by uid so two users on one machine do not collide.
func sockPath(kind string) string {
	dir := os.TempDir()
	return filepath.Join(dir, fmt.Sprintf("safenat-%s-%d.sock", kind, os.Getuid()))
}
