package ext

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	p, err := Install(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Install(dir)
	if err != nil || again != p {
		t.Fatalf("second install: %s %v", again, err)
	}
	b, _ := os.ReadFile(p)
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"META-INF/sisu/javax.inject.Named": false, "dev/buildyard/ext/BuildyardEventSpy.class": false}
	for _, f := range z.File {
		if _, ok := want[f.Name]; ok {
			want[f.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("jar has no %s; rebuild it with make ext", name)
		}
	}
}
