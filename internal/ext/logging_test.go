package ext

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestMavenConf(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "conf/settings.xml"), "<settings/>")
	write(t, filepath.Join(home, "conf/logging/simplelogger.properties"), "x=1")
	dir := t.TempDir()
	conf, err := MavenConf(dir, "maven3", home)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := MavenConf(dir, "maven3", home); err != nil || again != conf {
		t.Fatalf("second call: %s %v", again, err)
	}
	if b, err := os.ReadFile(filepath.Join(conf, "settings.xml")); err != nil || string(b) != "<settings/>" {
		t.Errorf("settings.xml not linked: %q %v", b, err)
	}
	for _, f := range []string{"logging/simplelogger.properties", "logging/org/slf4j/impl/StaticLoggerBinder.class",
		"logging/dev/buildyard/log/JsonSink.class", "logging/META-INF/maven/slf4j-configuration.properties"} {
		if _, err := os.Stat(filepath.Join(conf, f)); err != nil {
			t.Errorf("%s: %v (rebuild the loggers with make ext)", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(conf, "logging/META-INF/MANIFEST.MF")); err == nil {
		t.Error("the jar's manifest is not copied")
	}
	m4, err := MavenConf(dir, "maven4", home)
	if err != nil || m4 == conf {
		t.Fatalf("maven4: %s %v", m4, err)
	}
	if _, err := os.Stat(filepath.Join(m4, "logging/META-INF/services/org.apache.maven.slf4j.MavenLoggerFactory")); err != nil {
		t.Error(err)
	}
}

func TestMvndHome(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "bin/mvnd"), "native client")
	write(t, filepath.Join(home, "bin/mvnd-daemon.conf"), "daemon")
	write(t, filepath.Join(home, "mvn/conf/settings.xml"), "<settings/>")
	write(t, filepath.Join(home, "mvn/lib/x.jar"), "jar")
	client, err := MvndHome(t.TempDir(), "mvnd1", home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(client)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("client must be a real executable file, not a symlink: %v %v", st.Mode(), err)
	}
	out := filepath.Dir(filepath.Dir(client))
	for _, f := range []string{"bin/mvnd-daemon.conf", "mvn/lib/x.jar", "mvn/conf/settings.xml", "mvn/conf/logging/org/slf4j/impl/BuildyardMvndLoggerFactory.class"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := MvndHome(t.TempDir(), "nope", home); err == nil {
		t.Error("unknown variant")
	}
}
