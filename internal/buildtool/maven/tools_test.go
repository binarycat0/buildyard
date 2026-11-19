package maven

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, p string, exec bool) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	os.WriteFile(p, []byte("x"), mode)
}

func TestVersionsFromInstallFiles(t *testing.T) {
	root := t.TempDir()
	// Homebrew maven: bin/mvn → Cellar/maven/3.9.11/libexec/bin/mvn, jars in libexec/lib.
	mvn := filepath.Join(root, "Cellar/maven/3.9.11/libexec/bin/mvn")
	touch(t, mvn, true)
	touch(t, filepath.Join(root, "Cellar/maven/3.9.11/libexec/lib/maven-core-3.9.11.jar"), false)
	if v := findMavenVersion(mvn); v != "3.9.11" {
		t.Fatalf("mvn version %q", v)
	}

	// mvnd distribution: bin/mvnd, mvn/lib/maven-core-3.9.9.jar, mvn/lib/ext/mvnd-daemon-1.0.2.jar.
	dist := filepath.Join(root, "maven-mvnd-1.0.2-darwin-aarch64")
	mvnd := filepath.Join(dist, "bin/mvnd")
	touch(t, mvnd, true)
	touch(t, filepath.Join(dist, "mvn/lib/maven-core-3.9.9.jar"), false)
	touch(t, filepath.Join(dist, "mvn/lib/ext/mvnd-daemon-1.0.2.jar"), false)
	if v := findMvndVersion(mvnd); v != "1.0.2" {
		t.Fatalf("mvnd version %q", v)
	}
	if v := findMavenVersion(mvnd); v != "3.9.9" {
		t.Fatalf("mvnd maven version %q", v)
	}

	// Only the directory name is known.
	bare := filepath.Join(root, "Cellar/mvnd/1.0.3/bin/mvnd")
	touch(t, bare, true)
	if v := findMvndVersion(bare); v != "1.0.3" {
		t.Fatalf("mvnd from dir %q", v)
	}
}

func TestDetectToolsViaSdkman(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	old := systemBinDirs
	systemBinDirs = nil // the CI machine may have Homebrew's own Maven
	t.Cleanup(func() { systemBinDirs = old })
	touch(t, filepath.Join(home, ".sdkman/candidates/maven/3.9.9/bin/mvn"), true)
	touch(t, filepath.Join(home, ".sdkman/candidates/maven/3.9.9/lib/maven-core-3.9.9.jar"), false)
	os.Symlink(filepath.Join(home, ".sdkman/candidates/maven/3.9.9"), filepath.Join(home, ".sdkman/candidates/maven/current"))

	tools := DetectTools(home)
	mvn := tools[0]
	if !mvn.Installed || mvn.Version != "3.9.9" {
		t.Fatalf("mvn: %+v", mvn)
	}
	if tools[1].Installed && tools[1].Path == "" {
		t.Fatalf("mvnd: %+v", tools[1])
	}
}
