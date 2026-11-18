package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/binarycat0/buildyard/internal/service"
)

const serviceUsage = "usage: buildyard service install|uninstall|start|stop|restart|status"

func cmdService(args []string) error {
	if len(args) != 1 {
		return errors.New(serviceUsage)
	}
	m, err := service.ForPlatform()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		e, err := setup()
		if err != nil {
			return err
		}
		spec, err := serviceSpec(e)
		if err != nil {
			return err
		}
		if err := m.Install(spec); err != nil {
			return err
		}
		fmt.Printf("installed %s service: %s\n", m.Name(), m.Path())
		fmt.Println("runs:", spec.Executable, "serve --service")
		fmt.Println("log:", spec.LogPath)
		fmt.Println("next: `buildyard service start`")
		return nil
	case "uninstall":
		if err := m.Uninstall(); err != nil {
			return err
		}
		fmt.Println("uninstalled", m.Path())
		return nil
	case "start":
		if err := m.Start(); err != nil {
			return err
		}
		if e, err := setup(); err == nil {
			fmt.Println("started; open", link(e))
		} else {
			fmt.Println("started")
		}
		return nil
	case "restart":
		if err := m.Restart(); err != nil {
			return err
		}
		fmt.Println("restarted")
		return nil
	case "stop":
		if err := m.Stop(); err != nil {
			return err
		}
		fmt.Println("stopped (running builds were interrupted)")
		return nil
	case "status":
		s, err := m.Status()
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s (%s)\n", m.Name(), s, m.Path())
		return nil
	}
	return fmt.Errorf("unknown service command %q\n%s", args[0], serviceUsage)
}

// serviceEnv is what the service carries over from the shell that installed
// it. launchd and systemd start with a bare environment, so without PATH the
// daemon would not find mvn, mvnd, git or java.
var serviceEnv = []string{"PATH", "USER", "JAVA_HOME", "BUILDYARD_HOME"}

// serviceSpec points the service at this binary's real path (not a symlink,
// so an upgrade that replaces the binary is picked up on restart).
func serviceSpec(e env) (service.Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return service.Spec{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return service.Spec{}, err
	}
	envs := map[string]string{}
	for _, k := range serviceEnv {
		if v := os.Getenv(k); v != "" {
			envs[k] = v
		}
	}
	// The data directory is resolved now, so a relative BUILDYARD_HOME still
	// points at the same place when the service starts elsewhere.
	if _, ok := envs["BUILDYARD_HOME"]; ok {
		envs["BUILDYARD_HOME"] = e.paths.Dir
	}
	return service.Spec{Executable: exe, Args: []string{"serve", "--service"}, Env: envs, LogPath: e.paths.Log}, nil
}
