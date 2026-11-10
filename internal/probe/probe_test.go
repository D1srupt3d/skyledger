package probe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTouchCreatesThenRefreshes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "alive")
	Touch(p)
	if _, err := os.Stat(p); err != nil {
		t.Fatal("not created:", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	Touch(p)
	st, _ := os.Stat(p)
	if time.Since(st.ModTime()) > time.Minute {
		t.Errorf("mtime not refreshed: %v", st.ModTime())
	}
}

func TestTouchUnwritableIsIgnored(t *testing.T) {
	Touch(filepath.Join(t.TempDir(), "missing-dir", "alive")) // must not panic
}
