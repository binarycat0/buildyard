// Package ext embeds Buildyard's Maven extension (maven-ext/, rebuilt with `make ext`).
// The jar is committed so `go build` works without Maven.
package ext

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
)

//go:embed buildyard-maven-ext.jar
var jar []byte

// Install writes the jar into dir under a content-hashed name (so a running mvnd daemon
// never sees a changed file under an old name) and returns its path.
func Install(dir string) (string, error) {
	sum := sha256.Sum256(jar)
	path := filepath.Join(dir, "buildyard-maven-ext-"+hex.EncodeToString(sum[:4])+".jar")
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(jar)) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, jar, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
