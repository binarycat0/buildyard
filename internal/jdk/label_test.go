package jdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabel(t *testing.T) {
	tests := map[string]string{
		"/opt/homebrew/Cellar/openjdk@21/21.0.8/libexec/openjdk.jdk/Contents/Home":  "openjdk@21 21.0.8 (Homebrew)",
		"/opt/homebrew/Cellar/openjdk/24.0.1/libexec/openjdk.jdk/Contents/Home":     "openjdk 24.0.1 (Homebrew)",
		"/home/linuxbrew/.linuxbrew/Cellar/openjdk@17/17.0.16/libexec":              "openjdk@17 17.0.16 (Homebrew)",
		"/Users/me/.sdkman/candidates/java/21.0.7-tem":                              "21.0.7-tem (sdkman)",
		"/Users/me/.asdf/installs/java/temurin-21.0.5+11.0.LTS":                     "temurin-21.0.5+11.0.LTS (asdf)",
		"/Users/me/.local/share/mise/installs/java/21.0.2":                          "21.0.2 (mise)",
		"/Users/me/.gradle/jdks/eclipse_adoptium-17-aarch64-os_x/Contents/Home":     "eclipse_adoptium-17-aarch64-os_x (Gradle)",
		"/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home":            "temurin-21",
		"/Users/me/Library/Java/JavaVirtualMachines/corretto-17.0.13/Contents/Home": "corretto-17.0.13",
		"/usr/lib/jvm/java-21-openjdk-amd64":                                        "java-21-openjdk-amd64",
		"/opt/graalvm":                                                              "graalvm",
	}
	for in, want := range tests {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseProperties(t *testing.T) {
	out := []byte(`Property settings:
    file.separator = /
    java.class.path = 
    java.library.path = /Users/me/Library/Java/Extensions
        /Library/Java/Extensions
    java.vendor = Homebrew
    java.version = 21.0.8
    os.arch = aarch64

openjdk version "21.0.8" 2025-07-15
`)
	p := parseProperties(out)
	if p["java.version"] != "21.0.8" || p["java.vendor"] != "Homebrew" || p["os.arch"] != "aarch64" {
		t.Fatalf("%v", p)
	}
}

func TestInspectHomebrewOptLinkAndProbeFallback(t *testing.T) {
	root := t.TempDir()
	// Homebrew layout: opt/openjdk@21 → ../Cellar/openjdk@21/21.0.8, no release file.
	home := filepath.Join(root, "Cellar/openjdk@21/21.0.8/libexec/openjdk.jdk/Contents/Home")
	os.MkdirAll(filepath.Join(home, "bin"), 0o755)
	os.WriteFile(filepath.Join(home, "bin/java"), []byte("#!/bin/sh\n"), 0o755)
	os.MkdirAll(filepath.Join(root, "opt"), 0o755)
	os.Symlink(filepath.Join(root, "Cellar/openjdk@21/21.0.8"), filepath.Join(root, "opt/openjdk@21"))

	old := probe
	defer func() { probe = old }()
	probe = func(java string) (map[string]string, error) {
		return map[string]string{"java.version": "21.0.8", "java.vendor": "Homebrew", "os.arch": "aarch64"}, nil
	}

	found := filepath.Join(root, "opt/openjdk@21/libexec/openjdk.jdk/Contents/Home")
	info, err := Inspect(found)
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.EvalSymlinks(home)
	if info.Home != wantHome || info.FoundAt != found {
		t.Fatalf("home %q foundAt %q", info.Home, info.FoundAt)
	}
	if info.Label != "openjdk@21 21.0.8 (Homebrew)" || info.Version != "21.0.8" || info.Major != 21 || info.Vendor != "Homebrew" {
		t.Fatalf("%+v", info)
	}
}

func TestManagers(t *testing.T) {
	root := t.TempDir()
	mk := func(home, version string) string {
		os.MkdirAll(filepath.Join(home, "bin"), 0o755)
		os.WriteFile(filepath.Join(home, "bin/java"), []byte("#!/bin/sh\n"), 0o755)
		os.WriteFile(filepath.Join(home, "release"), []byte(`JAVA_VERSION="`+version+`"`), 0o644)
		return home
	}
	registered := mk(filepath.Join(root, "Library/Java/JavaVirtualMachines/zulu-17.jdk/Contents/Home"), "17.0.13")
	brewPrefix := filepath.Join(root, "custom-brew")
	mk(filepath.Join(brewPrefix, "Cellar/openjdk@21/21.0.8/libexec/openjdk.jdk/Contents/Home"), "21.0.8")
	os.MkdirAll(filepath.Join(brewPrefix, "opt"), 0o755)
	os.Symlink(filepath.Join(brewPrefix, "Cellar/openjdk@21/21.0.8"), filepath.Join(brewPrefix, "opt/openjdk@21"))
	sdkman := filepath.Join(root, "sdk")
	mk(filepath.Join(sdkman, "candidates/java/11.0.25-tem"), "11.0.25")

	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><array><dict>
  <key>JVMArch</key><string>arm64</string>
  <key>JVMHomePath</key><string>` + registered + `</string>
  <key>JVMName</key><string>Zulu 17.54.21</string>
</dict></array></plist>`

	d := Detector{
		Home: filepath.Join(root, "home"), GOOS: "darwin", SystemRoot: filepath.Join(root, "sys"),
		Run: func(name string, args ...string) ([]byte, error) {
			switch name {
			case "/usr/libexec/java_home":
				return []byte(plist), nil
			case "/fake/brew":
				return []byte(brewPrefix + "\n"), nil
			}
			return nil, os.ErrNotExist
		},
		LookPath: func(string) string { return "/fake/brew" },
		Getenv: func(k string) string {
			if k == "SDKMAN_DIR" {
				return sdkman
			}
			return ""
		},
	}
	got := d.Detect()
	labels := map[string]string{}
	for _, j := range got {
		labels[j.Version] = j.Label + " / " + j.Source
	}
	want := map[string]string{
		"21.0.8":  "openjdk@21 21.0.8 (Homebrew) / homebrew",
		"17.0.13": "zulu-17 / macos",
		"11.0.25": "11.0.25-tem (sdkman) / sdkman",
	}
	if len(got) != 3 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	for v, w := range want {
		if labels[v] != w {
			t.Errorf("%s: got %q, want %q", v, labels[v], w)
		}
	}
	for _, j := range got {
		if j.Version == "21.0.8" && !strings.Contains(j.FoundAt, "custom-brew/opt/openjdk@21") {
			t.Errorf("brew foundAt = %q", j.FoundAt)
		}
	}
}
