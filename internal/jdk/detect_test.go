package jdk

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeJDK(t *testing.T, home, version, vendor string) string {
	t.Helper()
	os.MkdirAll(filepath.Join(home, "bin"), 0o755)
	os.WriteFile(filepath.Join(home, "bin/java"), []byte("#!/bin/sh\n"), 0o755)
	if version != "" {
		rel := "JAVA_VERSION=\"" + version + "\"\nIMPLEMENTOR=\"" + vendor + "\"\nOS_ARCH=\"aarch64\"\n"
		os.WriteFile(filepath.Join(home, "release"), []byte(rel), 0o644)
	}
	return home
}

func TestMajor(t *testing.T) {
	for in, want := range map[string]int{"21.0.8": 21, "17": 17, "1.8.0_402": 8, "11.0.2+9": 11, "": 0, "x": 0} {
		if got := Major(in); got != want {
			t.Errorf("Major(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestDetectDarwinAndSdkman(t *testing.T) {
	h := t.TempDir()
	temurin21 := fakeJDK(t, filepath.Join(h, ".sdkman/candidates/java/21.0.7-tem"), "21.0.7", "Eclipse Adoptium")
	fakeJDK(t, filepath.Join(h, ".sdkman/candidates/java/17.0.12-tem"), "17.0.12", "Eclipse Adoptium")
	// sdkman "current" is a symlink to another entry and must not be listed twice.
	os.Symlink(temurin21, filepath.Join(h, ".sdkman/candidates/java/current"))
	// jenv entries are symlinks to real homes; this one duplicates the sdkman 17.
	os.MkdirAll(filepath.Join(h, ".jenv/versions"), 0o755)
	os.Symlink(filepath.Join(h, ".sdkman/candidates/java/17.0.12-tem"), filepath.Join(h, ".jenv/versions/17"))
	// A macOS bundle in the user's Library.
	fakeJDK(t, filepath.Join(h, "Library/Java/JavaVirtualMachines/zulu-11.jdk/Contents/Home"), "11.0.24", "Azul Systems, Inc.")
	// Not a JDK: no bin/java.
	os.MkdirAll(filepath.Join(h, ".sdkman/candidates/java/broken"), 0o755)
	// JAVA_HOME without a release file still counts.
	noRelease := fakeJDK(t, filepath.Join(h, "custom-jdk"), "", "")

	got := isolated(Detector{Home: h, GOOS: "darwin", JavaHome: noRelease}, t).Detect()
	type row struct {
		ver, src string
	}
	want := []row{{"21.0.7", "sdkman"}, {"17.0.12", "sdkman"}, {"11.0.24", "macos"}, {"", "JAVA_HOME"}}
	if len(got) != len(want) {
		t.Fatalf("got %d JDKs: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Version != w.ver || got[i].Source != w.src {
			t.Errorf("[%d] = %s/%s, want %s/%s", i, got[i].Version, got[i].Source, w.ver, w.src)
		}
	}
	if got[0].Vendor != "Eclipse Adoptium" || got[0].Arch != "aarch64" || got[0].Major != 21 {
		t.Errorf("release not parsed: %+v", got[0])
	}
}

func TestInspectAcceptsBundlePath(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "temurin-21.jdk")
	fakeJDK(t, filepath.Join(bundle, "Contents/Home"), "21.0.8", "Eclipse Adoptium")
	info, err := Inspect(bundle)
	if err != nil || filepath.Base(info.Home) != "Home" || info.Major != 21 {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := Inspect(t.TempDir()); err != ErrNotJDK {
		t.Fatalf("empty dir: %v", err)
	}
}

// isolated keeps a detector away from the real machine: no commands, env, brew or system dirs.
func isolated(d Detector, t *testing.T) Detector {
	d.Run = func(string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	d.Getenv = func(string) string { return "" }
	d.LookPath = func(string) string { return "" }
	d.SystemRoot = t.TempDir()
	return d
}
