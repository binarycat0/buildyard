package maven

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectLogRuntime(t *testing.T) {
	root := t.TempDir()
	maven := func(dir, v string) string {
		touch(t, filepath.Join(dir, "bin/m2.conf"), false)
		touch(t, filepath.Join(dir, "lib/maven-core-"+v+".jar"), false)
		touch(t, filepath.Join(dir, "bin/mvn"), true)
		return filepath.Join(dir, "bin/mvn")
	}
	// Homebrew: bin/mvn in Cellar/maven/<v>/bin, the installation under libexec.
	brew := filepath.Join(root, "Cellar/maven/3.9.11")
	maven(filepath.Join(brew, "libexec"), "3.9.11")
	touch(t, filepath.Join(brew, "bin/mvn"), true)
	rt, ok := DetectLogRuntime(Resolved{Kind: ModeMvn, Path: filepath.Join(brew, "bin/mvn")}, root, root)
	if !ok || rt.Variant != LogMaven3 || rt.Home != filepath.Join(brew, "libexec") || rt.Version != "3.9.11" {
		t.Fatalf("brew maven: %+v %v", rt, ok)
	}
	if _, ok := DetectLogRuntime(Resolved{Kind: ModeMvn, Path: maven(filepath.Join(root, "m385"), "3.8.5")}, root, root); ok {
		t.Error("untested Maven 3.8.5 must run without the logger")
	}
	rt, ok = DetectLogRuntime(Resolved{Kind: "custom", Path: maven(filepath.Join(root, "m4"), "4.0.0-rc-7")}, root, root)
	if !ok || rt.Variant != LogMaven4 {
		t.Errorf("maven 4: %+v %v", rt, ok)
	}

	// mvnd 1.0.6 distribution.
	mvnd := filepath.Join(root, "maven-mvnd-1.0.6-darwin-aarch64")
	touch(t, filepath.Join(mvnd, "bin/mvnd"), true)
	touch(t, filepath.Join(mvnd, "bin/mvnd-daemon.conf"), false)
	touch(t, filepath.Join(mvnd, "mvn/bin/m2.conf"), false)
	touch(t, filepath.Join(mvnd, "mvn/lib/mvnd/mvnd-client-1.0.6.jar"), false)
	rt, ok = DetectLogRuntime(Resolved{Kind: ModeMvnd, Path: filepath.Join(mvnd, "bin/mvnd")}, root, root)
	if !ok || rt.Variant != LogMvnd1 || rt.Home != mvnd || rt.Version != "1.0.6" {
		t.Errorf("mvnd: %+v %v", rt, ok)
	}

	if rt, ok := DetectLogRuntime(Resolved{Kind: "custom", Path: filepath.Join(mvnd, "bin/mvnd")}, root, root); !ok || rt.Variant != LogMvnd1 {
		t.Errorf("mvnd as a custom path: %+v %v", rt, ok)
	}

	// ./mvnw: only once the distribution is downloaded.
	proj := filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(proj, ".mvn/wrapper"), 0o755)
	os.WriteFile(filepath.Join(proj, ".mvn/wrapper/maven-wrapper.properties"),
		[]byte("distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.9/apache-maven-3.9.9-bin.zip\n"), 0o644)
	t.Setenv("MAVEN_USER_HOME", "")
	w := Resolved{Kind: ModeWrapper, Path: filepath.Join(proj, "mvnw")}
	if _, ok := DetectLogRuntime(w, proj, root); ok {
		t.Error("wrapper distribution not downloaded yet")
	}
	dist := filepath.Join(root, ".m2/wrapper/dists/apache-maven-3.9.9-bin/abc123/apache-maven-3.9.9")
	maven(dist, "3.9.9")
	rt, ok = DetectLogRuntime(w, proj, root)
	if !ok || rt.Home != dist {
		t.Errorf("wrapper: %+v %v", rt, ok)
	}
}
