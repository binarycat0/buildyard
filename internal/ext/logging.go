package ext

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Buildyard's JSON loggers (maven-log/, rebuilt with `make ext`), one per runtime. Maven's
// launchers load ${maven.conf}/logging before their own jars, so a copy of the conf
// folder whose logging/ also holds a logger's classes puts that logger in charge, without
// changing the project or the Maven installation.

//go:embed maven-log-maven3.jar
var logMaven3 []byte

//go:embed maven-log-maven4.jar
var logMaven4 []byte

//go:embed maven-log-mvnd1.jar
var logMvnd1 []byte

var loggers = map[string][]byte{"maven3": logMaven3, "maven4": logMaven4, "mvnd1": logMvnd1}

// MavenConf returns a conf folder for the Maven installation at home with the variant's
// logger in it, made under dir on first use. A Maven run picks it up from
// -Dmaven.conf=<it> in MAVEN_OPTS.
func MavenConf(dir, variant, home string) (string, error) {
	jar, ok := loggers[variant]
	if !ok {
		return "", fmt.Errorf("unknown logger %q", variant)
	}
	return cached(dir, "conf", variant, home, jar, func(out string) error {
		return mirrorConf(filepath.Join(home, "conf"), out, jar)
	})
}

// MvndHome returns a stand-in for the mvnd installation at home whose daemon loads the
// variant's logger, made under dir on first use, and the mvnd client to run from it. The
// client finds its installation from its own path, so the stand-in links everything to
// the real one except mvn/conf (generated) and the client binary (a hard link, or a copy
// across file systems: through a symlink it would find the real installation).
func MvndHome(dir, variant, home string) (string, error) {
	jar, ok := loggers[variant]
	if !ok {
		return "", fmt.Errorf("unknown logger %q", variant)
	}
	out, err := cached(dir, "mvnd", variant, home, jar, func(out string) error {
		if err := linkAll(home, out, "bin", "mvn"); err != nil {
			return err
		}
		if err := linkAll(filepath.Join(home, "bin"), filepath.Join(out, "bin"), "mvnd"); err != nil {
			return err
		}
		if err := linkOrCopy(filepath.Join(home, "bin", "mvnd"), filepath.Join(out, "bin", "mvnd")); err != nil {
			return err
		}
		if err := linkAll(filepath.Join(home, "mvn"), filepath.Join(out, "mvn"), "conf"); err != nil {
			return err
		}
		return mirrorConf(filepath.Join(home, "mvn", "conf"), filepath.Join(out, "mvn", "conf"), jar)
	})
	if err != nil {
		return "", err
	}
	return filepath.Join(out, "bin", "mvnd"), nil
}

// cached builds a folder once per (kind, variant, installation, logger build) and reuses it.
func cached(dir, kind, variant, home string, jar []byte, build func(out string) error) (string, error) {
	h := sha256.New()
	h.Write([]byte(home))
	h.Write(jar)
	out := filepath.Join(dir, kind+"-"+variant+"-"+hex.EncodeToString(h.Sum(nil)[:6]))
	if st, err := os.Stat(out); err == nil && st.IsDir() {
		return out, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	work := filepath.Join(tmp, "out")
	if err := build(work); err != nil {
		return "", err
	}
	if err := os.Rename(work, out); err != nil {
		if st, serr := os.Stat(out); serr == nil && st.IsDir() {
			return out, nil // another job made it first
		}
		return "", err
	}
	return out, nil
}

// mirrorConf links every entry of a conf folder into out except logging/, which is copied
// and gets the logger's classes.
func mirrorConf(src, out string, jar []byte) error {
	if err := linkAll(src, out, "logging"); err != nil {
		return err
	}
	logging := filepath.Join(out, "logging")
	if err := os.MkdirAll(logging, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(src, "logging"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := copyFile(filepath.Join(src, "logging", e.Name()), filepath.Join(logging, e.Name())); err != nil {
				return err
			}
		}
	}
	return unzip(jar, logging)
}

// linkAll symlinks every entry of src into out (created), except the names in skip.
func linkAll(src, out string, skip ...string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if contains(skip, e.Name()) {
			continue
		}
		if err := os.Symlink(filepath.Join(src, e.Name()), filepath.Join(out, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// unzip extracts a logger jar's classes and service files into dir (not its manifest or
// Maven's pom copies, which would only clutter Maven's classpath).
func unzip(jar []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(jar), int64(len(jar)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") || name == "META-INF/MANIFEST.MF" || strings.HasPrefix(name, "META-INF/maven/dev.buildyard/") {
			continue
		}
		if strings.Contains(name, "..") {
			return fmt.Errorf("bad entry %q", name)
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
