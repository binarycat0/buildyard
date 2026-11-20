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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/binarycat0/buildyard/internal/client"
	"github.com/binarycat0/buildyard/internal/config"
	"github.com/binarycat0/buildyard/internal/ext"
	"github.com/binarycat0/buildyard/internal/httpapi"
	"github.com/binarycat0/buildyard/internal/jdk"
	"github.com/binarycat0/buildyard/internal/lock"
	"github.com/binarycat0/buildyard/internal/runner"
	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/tasks"
	"github.com/binarycat0/buildyard/internal/telemetry"
	"github.com/binarycat0/buildyard/web"
)

var version = "dev"

const usage = `Usage: buildyard <command> [flags]

Commands:
  serve     start the daemon on 127.0.0.1 and print the UI address
  open      print the UI address and open it in the browser
  service install|uninstall|start|stop|restart|status
            run the daemon in the background (launchd on macOS, systemd --user on Linux)
  version   print the version

Talking to the running daemon:
  status                 daemon version and projects with their last job
  projects               list projects
  tasks [-p project]     list a project's saved tasks
  run [-p project] [-d] <task>
                         run a saved task and follow its log; exits non-zero
                         unless the build succeeds (-d: queue and print the job id)
  run [-d] -rerun <job>  run a job again with the same settings
  jobs [-p project] [-n N]
                         recent jobs
  logs [-f] <job>        print a job's log (-f: follow until it ends)
  cancel <job>           stop a queued or running job

-p takes a project id or name; without it the project is the one whose
folder contains the current directory.

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
	case "service":
		err = cmdService(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "projects":
		err = cmdProjects(os.Args[2:])
	case "tasks":
		err = cmdTasks(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "jobs":
		err = cmdJobs(os.Args[2:])
	case "logs":
		err = cmdLogs(os.Args[2:])
	case "cancel":
		err = cmdCancel(os.Args[2:])
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
		if errors.Is(err, client.ErrDaemonDown) {
			fmt.Fprintln(os.Stderr, "start it with `buildyard service start` or `buildyard serve`")
		}
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
	return fmt.Sprintf("http://127.0.0.1:%d/", daemonPort(e))
}

// daemonPort is the port the running daemon listens on: the one it wrote to
// daemon.port (it may have been started with --port), else config.toml's.
func daemonPort(e env) int {
	if b, err := os.ReadFile(e.paths.Port); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
			return p
		}
	}
	return e.cfg.Port
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 0, "port to listen on (default from config.toml, 7777)")
	asService := fs.Bool("service", false, "log to stderr only; the service manager appends it to daemon.log")
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
	if err := os.WriteFile(e.paths.Port, []byte(strconv.Itoa(e.cfg.Port)+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(e.paths.Port)

	// In the foreground the log goes to the terminal and daemon.log. Under a
	// service manager stderr is already redirected to daemon.log, so writing
	// the file too would log every line twice.
	var logOut io.Writer = os.Stderr
	if !*asService {
		logFile, err := os.OpenFile(e.paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer logFile.Close()
		logOut = io.MultiWriter(os.Stderr, logFile)
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
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

	if n, err := st.InterruptUnfinished(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Info("jobs left by the previous run marked interrupted", "count", n)
	}
	jobs := runner.New(st, e.paths.Logs, e.cfg.MaxConcurrentJobs, e.cfg.Retention.JobsPerProject, log)
	otel := telemetry.New(e.cfg.Telemetry, version, log)
	otelCtx, otelStop := context.WithCancel(context.Background())
	otelDone := make(chan struct{})
	go func() { defer close(otelDone); otel.Run(otelCtx) }()
	jobs.Tracer = func(j store.Job, l runner.Labels, head string) runner.Trace {
		if t := otel.StartJob(j, telemetry.Labels{Project: l.Project, Task: l.Task, Tool: l.Tool, GitHead: head}, j.Argv); t != nil {
			return t
		}
		return nil
	}
	// Limits edited in the UI, or in config.toml followed by SIGHUP, apply without a restart.
	live := config.NewLive(e.paths.Config, e.cfg)
	live.OnChange(func(c config.Config) {
		jobs.SetLimits(c.MaxConcurrentJobs, c.Retention.JobsPerProject)
		otel.Configure(c.Telemetry)
		log.Info("config applied", "maxConcurrentJobs", c.MaxConcurrentJobs, "jobsPerProject", c.Retention.JobsPerProject)
	})
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			if err := live.Reload(); err != nil {
				log.Warn("config.toml not reloaded", "err", err)
			}
		}
	}()

	extJar, err := ext.Install(e.paths.Ext)
	if err != nil {
		log.Warn("Maven extension not installed; module folders and dependencies will be missing", "err", err)
	}

	home, _ := os.UserHomeDir()
	httpapi.Version = version
	srv := &httpapi.Server{Store: st, Tasks: bg, Runner: jobs, Config: live, Telemetry: otel, ExtJar: extJar, JDKs: jdk.NewDetector(), Home: home, DataDir: e.paths.Dir, UI: web.FS(), Log: log}
	if _, err := srv.SubmitJDKDetect(); err != nil {
		log.Warn("JDK detection not started", "err", err)
	}
	if _, err := srv.SubmitToolsDetect(); err != nil {
		log.Warn("build tool detection not started", "err", err)
	}
	log.Info("buildyard started", "version", version, "addr", fmt.Sprintf("127.0.0.1:%d", e.cfg.Port), "dataDir", e.paths.Dir)
	if !*asService {
		fmt.Fprintf(os.Stderr, "\nOpen Buildyard: %s\n\n", link(e))
	}

	err = httpapi.ListenAndServe(ctx, e.cfg.Port, srv.Handler())
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	jobs.Shutdown(shut) // running builds are stopped and marked interrupted
	cancel()
	otelStop() // sends what is still queued, for a few seconds at most
	<-otelDone
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
