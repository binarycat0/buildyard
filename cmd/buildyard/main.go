// Command buildyard is a personal build manager for Maven projects: a local daemon with a web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/binarycat0/buildyard/internal/config"
	"github.com/binarycat0/buildyard/internal/httpapi"
	"github.com/binarycat0/buildyard/internal/jdk"
	"github.com/binarycat0/buildyard/internal/lock"
	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/tasks"
	"github.com/binarycat0/buildyard/web"
)

var version = "dev"

const usage = `Usage: buildyard <command> [flags]

Commands:
  serve     start the daemon on 127.0.0.1 and print the UI address
  open      print the UI address and open it in the browser
  version   print the version

Data lives in ~/.buildyard (override with BUILDYARD_HOME).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "open":
		err = open(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("buildyard", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildyard:", err)
		os.Exit(1)
	}
}

type env struct {
	paths config.Paths
	cfg   config.Config
}

func setup() (env, error) {
	dir, err := config.DataDir()
	if err != nil {
		return env{}, err
	}
	paths := config.PathsIn(dir)
	if err := paths.Ensure(); err != nil {
		return env{}, err
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return env{}, err
	}
	return env{paths: paths, cfg: cfg}, nil
}

func link(e env) string {
	return fmt.Sprintf("http://127.0.0.1:%d/", e.cfg.Port)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 0, "port to listen on (default from config.toml, 7777)")
	fs.Parse(args)

	e, err := setup()
	if err != nil {
		return err
	}
	if *port != 0 {
		e.cfg.Port = *port
		if err := e.cfg.Validate(); err != nil {
			return err
		}
	}

	l, err := lock.Acquire(e.paths.Lock)
	if err != nil {
		return err
	}
	defer l.Release()

	logFile, err := os.OpenFile(e.paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, logFile), nil))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, e.paths.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	bg := tasks.New(2, log)
	defer bg.Close()

	home, _ := os.UserHomeDir()
	httpapi.Version = version
	srv := &httpapi.Server{Store: st, Tasks: bg, JDKs: jdk.NewDetector(), Home: home, DataDir: e.paths.Dir, UI: web.FS(), Log: log}
	if _, err := srv.SubmitJDKDetect(); err != nil {
		log.Warn("JDK detection not started", "err", err)
	}
	log.Info("buildyard started", "version", version, "addr", fmt.Sprintf("127.0.0.1:%d", e.cfg.Port), "dataDir", e.paths.Dir)
	fmt.Fprintf(os.Stderr, "\nOpen Buildyard: %s\n\n", link(e))

	err = httpapi.ListenAndServe(ctx, e.cfg.Port, srv.Handler())
	log.Info("buildyard stopped")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func open(args []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	printOnly := fs.Bool("print", false, "only print the address")
	fs.Parse(args)

	e, err := setup()
	if err != nil {
		return err
	}
	u := link(e)
	fmt.Println(u)
	if *printOnly {
		return nil
	}
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	return exec.Command(cmd, u).Start()
}
