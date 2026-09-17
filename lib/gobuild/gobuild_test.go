// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package gobuild

import (
	"context"
	"expvar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
)

func TestIsModuleIndexObject(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"exact header", write("idx", []byte("go index v2\n")), true},
		{"header plus body", write("idxbody", append([]byte("go index v2\n"), "payload"...)), true},
		{"wrong magic", write("other", []byte("not an index blob at all")), false},
		{"truncated header", write("short", []byte("go index")), false},
		{"empty", write("empty", nil), false},
		{"missing file", filepath.Join(dir, "does-not-exist"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isModuleIndexObject(tc.path); got != tc.want {
				t.Errorf("isModuleIndexObject(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestPutSkipsModuleIndex verifies a module-index object is staged locally but
// never forwarded to S3. The skip returns before the S3 client is touched, so
// leaving S3Client nil is safe and proves no upload was attempted.
func TestPutSkipsModuleIndex(t *testing.T) {
	local, err := cachedir.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &S3Cache{
		Local:         local,
		MinUploadSize: 1, // ensure the size gate does not mask the module-index skip
	}

	body := append([]byte("go index v2\n"), "index payload"...)
	const (
		actionID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		outputID = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	)
	diskPath, err := s.Put(context.Background(), gocache.Object{
		ActionID: actionID,
		OutputID: outputID,
		Size:     int64(len(body)),
		Body:     strings.NewReader(string(body)),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The object must still be staged on local disk...
	if diskPath == "" {
		t.Fatal("Put returned empty diskPath; object was not staged locally")
	}
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("staged object not readable: %v", err)
	}

	// ...and counted as a module-index skip, not uploaded or size-skipped.
	if got := s.putSkipModIdx.Value(); got != 1 {
		t.Errorf("putSkipModIdx = %d, want 1", got)
	}
	if got := s.putSkipSmall.Value(); got != 0 {
		t.Errorf("putSkipSmall = %d, want 0", got)
	}
	if got := s.putS3Object.Value(); got != 0 {
		t.Errorf("putS3Object = %d, want 0", got)
	}
	if got := s.putS3Action.Value(); got != 0 {
		t.Errorf("putS3Action = %d, want 0", got)
	}

	// No background uploads should have been queued; Close must return promptly.
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSetMetricsRegistersModuleIndexSkip(t *testing.T) {
	var s S3Cache
	m := new(expvar.Map).Init()
	s.SetMetrics(context.Background(), m)
	if m.Get("put_skip_module_index") == nil {
		t.Error("SetMetrics did not register put_skip_module_index")
	}
}
