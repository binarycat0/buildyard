package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCatalog(t *testing.T, env map[string]string) Catalog {
	t.Helper()
	tick := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	return Catalog{Home: t.TempDir(), BackupDir: t.TempDir(),
		Getenv: func(k string) string { return env[k] },
		Now:    func() time.Time { tick = tick.Add(time.Second); return tick }}
}

func TestPathsAndEnvOverrides(t *testing.T) {
	c := testCatalog(t, map[string]string{"GRADLE_USER_HOME": "/g", "MVND_PROPERTIES_PATH": "/m/p.properties"})
	got := map[string]File{}
	for _, f := range c.List() {
		got[f.ID] = f
	}
	if f := got["maven.settings"]; f.Path != filepath.Join(c.Home, ".m2/settings.xml") || f.PathFrom != "default" || f.Exists {
		t.Errorf("settings: %+v", f)
	}
	if f := got["gradle.properties"]; f.Path != "/g/gradle.properties" || f.PathFrom != "GRADLE_USER_HOME" {
		t.Errorf("gradle: %+v", f)
	}
	if f := got["mvnd.properties"]; f.Path != "/m/p.properties" || f.PathFrom != "MVND_PROPERTIES_PATH" {
		t.Errorf("mvnd: %+v", f)
	}
	if _, err := c.Get("nope"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestWriteBacksUpAndDetectsConflicts(t *testing.T) {
	c := testCatalog(t, nil)
	// A missing file reads as empty and is created (with its directory) from baseSHA "".
	got, err := c.Read("mvnd.properties")
	if err != nil || got.Exists || got.SHA != "" {
		t.Fatalf("read missing: %+v %v", got, err)
	}
	got, backup, err := c.Write("mvnd.properties", "mvnd.maxHeapSize=4g\n", "")
	if err != nil || backup != "" || !got.Exists || got.Content != "mvnd.maxHeapSize=4g\n" {
		t.Fatalf("create: %+v %q %v", got, backup, err)
	}
	// A stale base is refused and nothing changes.
	if _, _, err := c.Write("mvnd.properties", "x=1\n", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	// Edited outside after loading: refused too.
	path := got.Path
	os.WriteFile(path, []byte("mvnd.maxHeapSize=6g\n"), 0o600)
	os.Chmod(path, 0o600)
	if _, _, err := c.Write("mvnd.properties", "x=1\n", got.SHA); !errors.Is(err, ErrConflict) {
		t.Fatalf("external edit: %v", err)
	}
	cur, _ := c.Read("mvnd.properties")
	got2, backup, err := c.Write("mvnd.properties", "mvnd.maxHeapSize=8g\n", cur.SHA)
	if err != nil || backup == "" {
		t.Fatalf("update: %v %q", err, backup)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode not kept: %v", st.Mode())
	}
	list, _ := c.Backups("mvnd.properties")
	if len(list) != 1 || list[0].Name != backup {
		t.Fatalf("backups: %+v", list)
	}
	old, err := c.ReadBackup("mvnd.properties", backup)
	if err != nil || old != "mvnd.maxHeapSize=6g\n" {
		t.Fatalf("backup content %q %v", old, err)
	}
	if _, err := c.ReadBackup("mvnd.properties", "../../etc/passwd"); err == nil {
		t.Fatal("path traversal in backup name accepted")
	}
	// Saving the same content again makes no backup.
	if _, b, err := c.Write("mvnd.properties", "mvnd.maxHeapSize=8g\n", got2.SHA); err != nil || b != "" {
		t.Fatalf("no-op write: %q %v", b, err)
	}
}

func TestWriteThroughSymlinkAndPrune(t *testing.T) {
	c := testCatalog(t, nil)
	real := filepath.Join(t.TempDir(), "dotfiles-bazelrc")
	os.WriteFile(real, []byte("build --jobs=4\n"), 0o644)
	link := filepath.Join(c.Home, ".bazelrc")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	for i := 0; i < KeepBackups+3; i++ {
		cur, err := c.Read("bazel.bazelrc")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := c.Write("bazel.bazelrc", cur.Content+"# "+strings.Repeat("x", i)+"\n", cur.SHA); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	if data, _ := os.ReadFile(real); !strings.HasPrefix(string(data), "build --jobs=4\n#") {
		t.Fatalf("target not written: %q", data)
	}
	if list, _ := c.Backups("bazel.bazelrc"); len(list) != KeepBackups {
		t.Fatalf("backups kept: %d", len(list))
	}
}

func TestValidateXML(t *testing.T) {
	c := testCatalog(t, nil)
	if _, _, err := c.Write("maven.settings", "<settings><mirrors></settings>", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("broken XML accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Home, ".m2/settings.xml")); err == nil {
		t.Fatal("broken XML written")
	}
	if _, _, err := c.Write("maven.settings", settingsTemplate, ""); err != nil {
		t.Fatalf("template: %v", err)
	}
	tc := Toolchains([]ToolchainJDK{{Home: "/jdk/21 & co", Version: "21.0.8", Vendor: "Eclipse Adoptium"}, {Home: "/jdk/17", Major: 17}})
	if err := Validate(FormatXML, tc); err != nil {
		t.Fatalf("toolchains: %v\n%s", err, tc)
	}
	for _, want := range []string{"<version>21.0.8</version>", "<vendor>Eclipse Adoptium</vendor>", "<jdkHome>/jdk/21 &amp; co</jdkHome>", "<version>17</version>"} {
		if !strings.Contains(tc, want) {
			t.Errorf("toolchains missing %s:\n%s", want, tc)
		}
	}
	if err := Validate(FormatProperties, "not = xml <"); err != nil {
		t.Errorf("properties: %v", err)
	}
}
