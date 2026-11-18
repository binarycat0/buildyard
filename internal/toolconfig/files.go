// Package toolconfig finds, reads and safely rewrites the user-level config files of
// Java build tools (Maven, mvnd, Gradle, Bazel), keeping a backup of every version it replaces.
package toolconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Formats of config files; they decide validation and the editor helpers.
const (
	FormatXML        = "xml"
	FormatProperties = "properties"
	FormatShell      = "shell"
	FormatRC         = "rc" // .bazelrc: one command and its flags per line
)

// File is one known config file.
type File struct {
	ID          string `json:"id"`          // e.g. maven.settings
	Tool        string `json:"tool"`        // maven | mvnd | gradle | bazel
	Name        string `json:"name"`        // file name for display
	Path        string `json:"path"`        // absolute path, after env overrides
	PathFrom    string `json:"pathFrom"`    // "default" or the env variable that moved it
	Format      string `json:"format"`      // xml | properties | shell | rc
	Description string `json:"description"` // what the file controls
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size"`
	ModTime     string `json:"modTime,omitempty"` // RFC 3339
	Template    string `json:"template"`          // starting content for a missing file
}

// Content is a file with its text and the hash the next save must be based on.
type Content struct {
	File
	Content string `json:"content"`
	SHA     string `json:"sha"` // "" when the file does not exist
}

// Backup is one saved earlier version of a file.
type Backup struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// Errors returned by Catalog.
var (
	ErrUnknown  = errors.New("unknown config file")
	ErrConflict = errors.New("the file changed on disk since it was loaded")
	ErrInvalid  = errors.New("invalid content")
)

// MaxSize is the largest file the editor reads or writes.
const MaxSize = 1 << 20

// KeepBackups is how many backups are kept per file.
const KeepBackups = 30

// Catalog resolves the known files for one user.
type Catalog struct {
	Home      string              // user home
	BackupDir string              // backups go to BackupDir/<file id>/
	Getenv    func(string) string // os.Getenv; replaced in tests
	Now       func() time.Time    // time.Now; replaced in tests
}

type spec struct {
	id, tool, name, format, desc, template string
	env                                    string                             // env variable that overrides the path, if any
	path                                   func(c Catalog, env string) string // default path (env value passed when set)
}

func homePath(rel string) func(Catalog, string) string {
	return func(c Catalog, _ string) string { return filepath.Join(c.Home, rel) }
}

func envFile(rel string) func(Catalog, string) string {
	return func(c Catalog, env string) string {
		if env != "" {
			return env
		}
		return filepath.Join(c.Home, rel)
	}
}

func envDir(dir, name string) func(Catalog, string) string {
	return func(c Catalog, env string) string {
		if env != "" {
			return filepath.Join(env, name)
		}
		return filepath.Join(c.Home, dir, name)
	}
}

var specs = []spec{
	{id: "maven.settings", tool: "maven", name: "settings.xml", format: FormatXML, path: homePath(".m2/settings.xml"),
		desc:     "User Maven settings: local repository, mirrors, servers and credentials, proxies, active profiles. Read by mvn, ./mvnw and mvnd.",
		template: settingsTemplate},
	{id: "maven.toolchains", tool: "maven", name: "toolchains.xml", format: FormatXML, path: homePath(".m2/toolchains.xml"),
		desc:     "JDKs that builds using maven-toolchains-plugin (or the compiler's toolchain support) can select by version and vendor.",
		template: toolchainsHeader + toolchainsFooter},
	{id: "maven.mavenrc", tool: "maven", name: ".mavenrc", format: FormatShell, path: homePath(".mavenrc"),
		desc:     "Shell script sourced by the mvn and ./mvnw launchers before starting Maven, e.g. to set MAVEN_OPTS or JAVA_HOME. mvnd does not read it.",
		template: "# Sourced by mvn and ./mvnw (not by mvnd).\n# export MAVEN_OPTS=\"-Xmx4g\"\n"},
	{id: "mvnd.properties", tool: "mvnd", name: "mvnd.properties", format: FormatProperties, env: "MVND_PROPERTIES_PATH", path: envFile(".m2/mvnd.properties"),
		desc:     "User mvnd settings: daemon heap, JVM arguments, threads, keep-alive and idle timeouts. mvnd ignores MAVEN_OPTS and .mavenrc; daemon JVM options go here.",
		template: "# mvnd user properties. Changes apply to newly started daemons (stop running ones to apply).\n# mvnd.maxHeapSize=8g\n"},
	{id: "gradle.properties", tool: "gradle", name: "gradle.properties", format: FormatProperties, env: "GRADLE_USER_HOME", path: envDir(".gradle", "gradle.properties"),
		desc:     "User Gradle properties: daemon JVM arguments, parallelism, caching. Overrides the project's gradle.properties.",
		template: "# Gradle user properties.\n# org.gradle.jvmargs=-Xmx4g\n"},
	{id: "bazel.bazelrc", tool: "bazel", name: ".bazelrc", format: FormatRC, path: homePath(".bazelrc"),
		desc:     "User Bazel options, one command and its flags per line (e.g. build --jobs=8). Read after the workspace .bazelrc.",
		template: "# User Bazel options: <command> <flags>\n# build --disk_cache=~/.cache/bazel-disk\n"},
	{id: "bazel.bazeliskrc", tool: "bazel", name: ".bazeliskrc", format: FormatProperties, path: homePath(".bazeliskrc"),
		desc:     "Bazelisk settings (KEY=value), e.g. USE_BAZEL_VERSION. A project's .bazelversion still wins.",
		template: "# Bazelisk settings.\n# USE_BAZEL_VERSION=latest\n"},
}

func (c Catalog) getenv(k string) string {
	if k == "" {
		return ""
	}
	if c.Getenv != nil {
		return c.Getenv(k)
	}
	return os.Getenv(k)
}

func (c Catalog) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Catalog) file(s spec) File {
	env := c.getenv(s.env)
	f := File{ID: s.id, Tool: s.tool, Name: s.name, Path: s.path(c, env), PathFrom: "default",
		Format: s.format, Description: s.desc, Template: s.template}
	if env != "" {
		f.PathFrom = s.env
	}
	if st, err := os.Stat(f.Path); err == nil && st.Mode().IsRegular() {
		f.Exists, f.Size, f.ModTime = true, st.Size(), st.ModTime().UTC().Format(time.RFC3339)
	}
	return f
}

// List returns every known file with its current state on disk.
func (c Catalog) List() []File {
	out := make([]File, 0, len(specs))
	for _, s := range specs {
		out = append(out, c.file(s))
	}
	return out
}

// Get returns one known file.
func (c Catalog) Get(id string) (File, error) {
	for _, s := range specs {
		if s.id == id {
			return c.file(s), nil
		}
	}
	return File{}, ErrUnknown
}

// Read returns a file's content; a missing file reads as empty.
func (c Catalog) Read(id string) (Content, error) {
	f, err := c.Get(id)
	if err != nil {
		return Content{}, err
	}
	data, err := readLimited(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return Content{File: f}, nil
	}
	if err != nil {
		return Content{}, err
	}
	return Content{File: f, Content: string(data), SHA: hash(data)}, nil
}

func readLimited(p string) ([]byte, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	data, err := io.ReadAll(io.LimitReader(fh, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, MaxSize)
	}
	return data, nil
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Validate checks content for the file's format. Only XML is checked for well-formedness;
// the other formats accept any text.
func Validate(format, content string) error {
	if len(content) > MaxSize {
		return fmt.Errorf("%w: larger than %d bytes", ErrInvalid, MaxSize)
	}
	if format != FormatXML || strings.TrimSpace(content) == "" {
		return nil
	}
	d := xml.NewDecoder(strings.NewReader(content))
	d.Strict = true
	roots := 0
	depth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			line, _ := d.InputPos()
			return fmt.Errorf("%w: XML error near line %d: %v", ErrInvalid, line, err)
		}
		switch tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if roots != 1 {
		return fmt.Errorf("%w: XML must have exactly one root element", ErrInvalid)
	}
	return nil
}

// Write replaces a file's content. baseSHA is the hash the editor loaded ("" for a file that
// did not exist); if the file changed since then, nothing is written and ErrConflict is
// returned. The current version is backed up first. A symlinked file (dotfile managers)
// is written through the link. It returns the backup name ("" when there was no file).
func (c Catalog) Write(id, content, baseSHA string) (Content, string, error) {
	f, err := c.Get(id)
	if err != nil {
		return Content{}, "", err
	}
	if err := Validate(f.Format, content); err != nil {
		return Content{}, "", err
	}
	target := f.Path
	if r, err := filepath.EvalSymlinks(f.Path); err == nil {
		target = r
	}
	old, err := readLimited(target)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Content{}, "", err
	}
	cur := ""
	if exists {
		cur = hash(old)
	}
	if cur != baseSHA {
		return Content{}, "", ErrConflict
	}
	mode := fs.FileMode(0o644)
	backup := ""
	if exists {
		if st, err := os.Stat(target); err == nil {
			mode = st.Mode().Perm()
		}
		if bytes.Equal(old, []byte(content)) {
			c2, err := c.Read(id)
			return c2, "", err
		}
		if backup, err = c.backup(f, old); err != nil {
			return Content{}, "", fmt.Errorf("backup failed, nothing written: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Content{}, "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".buildyard-*")
	if err != nil {
		return Content{}, "", err
	}
	_, werr := tmp.WriteString(content)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), mode)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), target)
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return Content{}, "", werr
	}
	out, err := c.Read(id)
	return out, backup, err
}

var backupName = regexp.MustCompile(`^\d{8}-\d{6}(?:-\d+)?\.[\w.\-]+$`)

func (c Catalog) backupDir(id string) string { return filepath.Join(c.BackupDir, id) }

func (c Catalog) backup(f File, data []byte) (string, error) {
	if c.BackupDir == "" {
		return "", errors.New("no backup directory")
	}
	dir := c.backupDir(f.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp := c.now().Format("20060102-150405")
	name := stamp + "." + strings.TrimPrefix(f.Name, ".")
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d.%s", stamp, i, strings.TrimPrefix(f.Name, "."))
	}
	// Backups may hold credentials (settings.xml), so only the user can read them.
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return "", err
	}
	c.prune(f.ID)
	return name, nil
}

func (c Catalog) prune(id string) {
	list, err := c.Backups(id)
	if err != nil || len(list) <= KeepBackups {
		return
	}
	for _, b := range list[KeepBackups:] {
		os.Remove(filepath.Join(c.backupDir(id), b.Name))
	}
}

// Backups lists a file's backups, newest first.
func (c Catalog) Backups(id string) ([]Backup, error) {
	if _, err := c.Get(id); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(c.backupDir(id))
	if errors.Is(err, fs.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		if !backupName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, Backup{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime().UTC().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// ReadBackup returns the content of one backup.
func (c Catalog) ReadBackup(id, name string) (string, error) {
	if _, err := c.Get(id); err != nil {
		return "", err
	}
	if !backupName.MatchString(name) {
		return "", fs.ErrNotExist
	}
	data, err := readLimited(filepath.Join(c.backupDir(id), name))
	return string(data), err
}
