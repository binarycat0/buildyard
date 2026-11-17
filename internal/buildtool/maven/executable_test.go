package maven

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	withWrapper := filepath.Join(dir, "with")
	os.MkdirAll(filepath.Join(withWrapper, ".mvn/wrapper"), 0o755)
	os.WriteFile(filepath.Join(withWrapper, "mvnw"), []byte("#!/bin/sh"), 0o755)
	os.WriteFile(filepath.Join(withWrapper, ".mvn/wrapper/maven-wrapper.properties"),
		[]byte("wrapperVersion=3.3.2\ndistributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.16/apache-maven-3.9.16-bin.zip\n"), 0o644)
	without := filepath.Join(dir, "without")
	os.MkdirAll(without, 0o755)
	custom := filepath.Join(dir, "my-mvn")
	os.WriteFile(custom, []byte("#!/bin/sh"), 0o755)
	notExec := filepath.Join(dir, "plain")
	os.WriteFile(notExec, nil, 0o644)

	all := Tools{Mvn: "/usr/bin/mvn", Mvnd: "/opt/homebrew/bin/mvnd"}
	onlyMvn := Tools{Mvn: "/usr/bin/mvn"}

	tests := []struct {
		name     string
		mode     string
		root     string
		tools    Tools
		wantKind string
		wantPath string
		wantErr  bool
	}{
		{"auto prefers wrapper", "", withWrapper, all, "wrapper", filepath.Join(withWrapper, "mvnw"), false},
		{"auto falls back to mvnd", "auto", without, all, "mvnd", all.Mvnd, false},
		{"auto falls back to mvn", "auto", without, onlyMvn, "mvn", "/usr/bin/mvn", false},
		{"auto nothing", "auto", without, Tools{}, "", "", true},
		{"wrapper", "wrapper", withWrapper, Tools{}, "wrapper", filepath.Join(withWrapper, "mvnw"), false},
		{"wrapper missing", "wrapper", without, all, "", "", true},
		{"mvnd over wrapper", "mvnd", withWrapper, all, "mvnd", all.Mvnd, false},
		{"mvnd missing", "mvnd", withWrapper, onlyMvn, "", "", true},
		{"mvn", "mvn", withWrapper, all, "mvn", "/usr/bin/mvn", false},
		{"custom", custom, without, Tools{}, "custom", custom, false},
		{"custom not executable", notExec, without, all, "", "", true},
		{"unknown", "gradle", without, all, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Resolve(tt.mode, tt.root, tt.tools)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if r.Kind != tt.wantKind || r.Path != tt.wantPath {
				t.Fatalf("got %s %s", r.Kind, r.Path)
			}
		})
	}

	if _, err := Resolve("mvnd", without, Tools{}); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("want ErrNotAvailable, got %v", err)
	}
	if w := FindWrapper(withWrapper); w == nil || w.MavenVersion != "3.9.16" {
		t.Fatalf("wrapper: %+v", w)
	}
}

func TestValidMode(t *testing.T) {
	for m, want := range map[string]bool{"auto": true, "wrapper": true, "mvnd": true, "mvn": true, "/x/mvn": true, "mvnw": false, "": false, "rel/mvn": false} {
		if ValidMode(m) != want {
			t.Errorf("ValidMode(%q) != %v", m, want)
		}
	}
}
