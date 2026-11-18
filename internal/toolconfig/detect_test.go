package toolconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func exe(t *testing.T, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
}

func TestDetectToolsViaSdkman(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	old := SystemBinDirs
	SystemBinDirs = nil
	defer func() { SystemBinDirs = old }()

	home := t.TempDir()
	g := filepath.Join(home, ".sdkman/candidates/gradle/8.10.2")
	exe(t, filepath.Join(g, "bin/gradle"))
	os.WriteFile(filepath.Join(g, "lib/gradle-launcher-8.10.2.jar"), nil, 0o644)
	os.Symlink(g, filepath.Join(home, ".sdkman/candidates/gradle/current"))

	tools := DetectTools(home)
	if tools[0].ID != "gradle" || !tools[0].Installed || tools[0].Version != "8.10.2" {
		t.Errorf("gradle: %+v", tools[0])
	}
	if tools[1].Installed || tools[2].Installed {
		t.Errorf("bazel found: %+v", tools[1:])
	}
}

func TestProjectPins(t *testing.T) {
	root := t.TempDir()
	if GradleWrapperVersion(root) != "" || BazelVersion(root) != "" {
		t.Fatal("pins without files")
	}
	exe(t, filepath.Join(root, "gradlew"))
	os.MkdirAll(filepath.Join(root, "gradle/wrapper"), 0o755)
	os.WriteFile(filepath.Join(root, "gradle/wrapper/gradle-wrapper.properties"),
		[]byte("distributionUrl=https\\://services.gradle.org/distributions/gradle-8.14.3-bin.zip\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".bazelversion"), []byte("7.4.1\n"), 0o644)
	if v := GradleWrapperVersion(root); v != "8.14.3" {
		t.Errorf("gradle wrapper: %q", v)
	}
	if v := BazelVersion(root); v != "7.4.1" {
		t.Errorf("bazel: %q", v)
	}
}
