// Package config resolves the Buildyard data directory and loads config.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultPort is the port the daemon listens on when config.toml does not set one.
const DefaultPort = 7777

// Config mirrors ~/.buildyard/config.toml.
type Config struct {
	Port              int       `toml:"port"`
	MaxConcurrentJobs int       `toml:"maxConcurrentJobs"`
	Retention         Retention `toml:"retention"`
	Defaults          Defaults  `toml:"defaults"`
}

type Retention struct {
	JobsPerProject int    `toml:"jobsPerProject"`
	MaxLogBytes    string `toml:"maxLogBytes"`
}

type Defaults struct {
	Executable string   `toml:"executable"` // auto | mvn | mvnd | mvnw
	Flags      []string `toml:"flags"`
}

// Default returns the configuration used when config.toml is missing or leaves fields unset.
func Default() Config {
	return Config{
		Port:              DefaultPort,
		MaxConcurrentJobs: 1,
		Retention:         Retention{JobsPerProject: 200, MaxLogBytes: "2GB"},
		Defaults:          Defaults{Executable: "auto", Flags: []string{"-ntp"}},
	}
}

// Paths are the files and directories under the data directory.
type Paths struct {
	Dir    string
	Config string
	Lock   string
	Log    string
	DB     string
	Logs   string
	Events string
	Ext    string
	Cache  string
}

// DataDir returns $BUILDYARD_HOME if set, otherwise ~/.buildyard.
func DataDir() (string, error) {
	if d := os.Getenv("BUILDYARD_HOME"); d != "" {
		return filepath.Abs(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, ".buildyard"), nil
}

// PathsIn lays out the data directory rooted at dir.
func PathsIn(dir string) Paths {
	return Paths{
		Dir:    dir,
		Config: filepath.Join(dir, "config.toml"),
		Lock:   filepath.Join(dir, "daemon.lock"),
		Log:    filepath.Join(dir, "daemon.log"),
		DB:     filepath.Join(dir, "buildyard.db"),
		Logs:   filepath.Join(dir, "logs"),
		Events: filepath.Join(dir, "events"),
		Ext:    filepath.Join(dir, "ext"),
		Cache:  filepath.Join(dir, "cache"),
	}
}

// Ensure creates the data directory and its subdirectories.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Dir, p.Logs, p.Events, p.Ext, p.Cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Load reads config.toml, filling unset fields with defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var file Config
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if md.IsDefined("port") {
		cfg.Port = file.Port
	}
	if md.IsDefined("maxConcurrentJobs") {
		cfg.MaxConcurrentJobs = file.MaxConcurrentJobs
	}
	if md.IsDefined("retention", "jobsPerProject") {
		cfg.Retention.JobsPerProject = file.Retention.JobsPerProject
	}
	if md.IsDefined("retention", "maxLogBytes") {
		cfg.Retention.MaxLogBytes = file.Retention.MaxLogBytes
	}
	if md.IsDefined("defaults", "executable") {
		cfg.Defaults.Executable = file.Defaults.Executable
	}
	if md.IsDefined("defaults", "flags") {
		cfg.Defaults.Flags = file.Defaults.Flags
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks values that would make the daemon misbehave.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range", c.Port)
	}
	if c.MaxConcurrentJobs < 1 {
		return fmt.Errorf("maxConcurrentJobs must be >= 1")
	}
	switch c.Defaults.Executable {
	case "auto", "mvn", "mvnd", "mvnw":
	default:
		return fmt.Errorf("defaults.executable must be auto, mvn, mvnd or mvnw")
	}
	return nil
}
