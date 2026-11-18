package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInspect(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, files ...string) string {
		d := filepath.Join(dir, name)
		os.MkdirAll(d, 0o755)
		for _, f := range files {
			os.WriteFile(filepath.Join(d, f), []byte("x"), 0o644)
		}
		return d
	}
	maven := mk("maven", "pom.xml")
	gradle := mk("gradle", "build.gradle.kts")
	empty := mk("empty")
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o644)

	tests := []struct {
		name    string
		path    string
		tool    string
		wantErr error
	}{
		{"maven", maven, "maven", nil},
		{"maven trailing slash", maven + "/", "maven", nil},
		{"gradle", gradle, "", ErrUnsupported},
		{"empty", empty, "", ErrNoBuildFile},
		{"relative", "maven", "", ErrNotAbsolute},
		{"missing", filepath.Join(dir, "nope"), "", ErrNotDir},
		{"file", file, "", ErrNotDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Inspect(tt.path)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if r.BuildTool != tt.tool {
				t.Fatalf("tool = %q, want %q", r.BuildTool, tt.tool)
			}
		})
	}
}
