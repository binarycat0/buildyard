package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/binarycat0/buildyard/internal/client"
)

// The commands in this file talk to the running daemon; none of them run
// builds themselves.

func newClient() (*client.Client, error) {
	e, err := setup()
	if err != nil {
		return nil, err
	}
	return client.New(daemonPort(e)), nil
}

// callCtx gives each plain call a deadline so a hung daemon can't hang the shell.
func callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Parse(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	defer cancel()
	h, err := c.Health(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("daemon %s, up %s, %s, data in %s\n", h.Version, time.Duration(h.UptimeSec)*time.Second, c.Base, h.DataDir)
	ps, err := c.Projects(ctx)
	if err != nil {
		return err
	}
	return printProjects(ps)
}

func cmdProjects(args []string) error {
	fs := flag.NewFlagSet("projects", flag.ExitOnError)
	fs.Parse(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	defer cancel()
	ps, err := c.Projects(ctx)
	if err != nil {
		return err
	}
	return printProjects(ps)
}

func printProjects(ps []client.Project) error {
	if len(ps) == 0 {
		fmt.Println("no projects yet; add one in the web UI")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROJECT\tLAST JOB\tPATH")
	for _, p := range ps {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", p.ID, p.Name, summary(p.LastJob), p.RootPath)
	}
	return tw.Flush()
}

func summary(j *client.JobSummary) string {
	if j == nil {
		return "-"
	}
	return fmt.Sprintf("#%d %s %s", j.ID, j.Status, ago(j.QueuedAt))
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func duration(j client.Job) string {
	if j.StartedAt == nil {
		return "-"
	}
	end := time.Now()
	if j.FinishedAt != nil {
		end = *j.FinishedAt
	}
	return end.Sub(*j.StartedAt).Round(time.Second).String()
}

// projectFlag adds -p to a command: a project id or name. Without it the
// project is the one whose folder contains the current directory.
func projectFlag(fs *flag.FlagSet) *string {
	return fs.String("p", "", "project id or name (default: the project containing the current directory)")
}

func resolveProject(ctx context.Context, c *client.Client, arg string) (client.Project, error) {
	ps, err := c.Projects(ctx)
	if err != nil {
		return client.Project{}, err
	}
	cwd, _ := os.Getwd()
	return pickProject(ps, arg, cwd)
}

func pickProject(ps []client.Project, arg, cwd string) (client.Project, error) {
	if arg != "" {
		if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
			for _, p := range ps {
				if p.ID == id {
					return p, nil
				}
			}
		}
		for _, p := range ps {
			if strings.EqualFold(p.Name, arg) {
				return p, nil
			}
		}
		return client.Project{}, fmt.Errorf("no project %q (see `buildyard projects`)", arg)
	}
	var best client.Project
	for _, p := range ps {
		root := filepath.Clean(p.RootPath)
		if (cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator))) && len(root) > len(best.RootPath) {
			best = p
			best.RootPath = root
		}
	}
	if best.ID == 0 {
		return best, errors.New("the current directory is not inside a project; pass -p <project>")
	}
	return best, nil
}

func pickTask(ts []client.Task, arg string) (client.Task, error) {
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, t := range ts {
			if t.ID == id {
				return t, nil
			}
		}
	}
	for _, t := range ts {
		if strings.EqualFold(t.Name, arg) {
			return t, nil
		}
	}
	return client.Task{}, fmt.Errorf("no task %q in this project (see `buildyard tasks`)", arg)
}

func cmdTasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	project := projectFlag(fs)
	fs.Parse(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	defer cancel()
	p, err := resolveProject(ctx, c, *project)
	if err != nil {
		return err
	}
	ts, err := c.Tasks(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		fmt.Printf("%s has no saved tasks; save one from the Run tab\n", p.Name)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTASK\tLAST JOB")
	for _, t := range ts {
		name := t.Name
		if t.Pinned {
			name += " *"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\n", t.ID, name, summary(t.LastJob))
	}
	return tw.Flush()
}

func cmdJobs(args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ExitOnError)
	project := projectFlag(fs)
	n := fs.Int("n", 15, "how many jobs to show")
	fs.Parse(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	defer cancel()
	p, err := resolveProject(ctx, c, *project)
	if err != nil {
		return err
	}
	js, err := c.Jobs(ctx, p.ID, *n)
	if err != nil {
		return err
	}
	tasks := map[int64]string{}
	if ts, err := c.Tasks(ctx, p.ID); err == nil {
		for _, t := range ts {
			tasks[t.ID] = t.Name
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB\tSTATUS\tQUEUED\tTIME\tTASK")
	for _, j := range js {
		task := "-"
		if j.ProfileID != nil {
			task = tasks[*j.ProfileID]
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", j.ID, j.Status, ago(j.QueuedAt), duration(j), task)
	}
	return tw.Flush()
}

// cmdRun queues a saved task (or reruns a job) and follows its log unless
// -d is given. The exit code is 0 only when the build succeeded, so it can
// gate scripts and git hooks.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	project := projectFlag(fs)
	detach := fs.Bool("d", false, "queue the job and return without following the log")
	rerun := fs.Int64("rerun", 0, "run again with the same settings as this job id instead of a task")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: buildyard run [-p project] [-d] <task>\n       buildyard run [-d] -rerun <job>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if (*rerun == 0) == (fs.NArg() != 1) {
		fs.Usage()
		os.Exit(2)
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	var job client.Job
	if *rerun != 0 {
		job, err = c.Rerun(ctx, *rerun)
	} else {
		var p client.Project
		if p, err = resolveProject(ctx, c, *project); err == nil {
			var ts []client.Task
			if ts, err = c.Tasks(ctx, p.ID); err == nil {
				var t client.Task
				if t, err = pickTask(ts, fs.Arg(0)); err == nil {
					job, err = c.RunTask(ctx, p.ID, t.ID)
				}
			}
		}
	}
	cancel()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "job %d %s: %s\n", job.ID, job.Status, strings.Join(job.Argv, " "))
	if *detach {
		fmt.Println(job.ID)
		return nil
	}
	return follow(c, job.ID, 1)
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	f := fs.Bool("f", false, "follow a running job until it ends")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: buildyard logs [-f] <job>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if fs.NArg() != 1 || err != nil {
		fs.Usage()
		os.Exit(2)
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	if *f {
		return follow(c, id, 1)
	}
	ctx, cancel := callCtx()
	defer cancel()
	for from := 1; ; {
		page, err := c.Log(ctx, id, from, 50000)
		if err != nil {
			return err
		}
		for _, l := range page.Lines {
			fmt.Println(l)
		}
		from += len(page.Lines)
		if len(page.Lines) == 0 || from > page.Total {
			return nil
		}
	}
}

func cmdCancel(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: buildyard cancel <job>")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("bad job id %q", args[0])
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	defer cancel()
	if err := c.Cancel(ctx, id); err != nil {
		return err
	}
	fmt.Println("cancelling job", id)
	return nil
}

// errJobFailed carries a finished job's status out as the exit code.
type errJobFailed struct{ job client.Job }

func (e errJobFailed) Error() string {
	return fmt.Sprintf("job %d %s", e.job.ID, e.job.Status)
}

// follow prints a job's log from line from until it ends. Ctrl-C stops
// following; the job keeps running in the daemon.
func follow(c *client.Client, id int64, from int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	job, err := c.Follow(ctx, id, from, func(_ int, text string) { fmt.Println(text) })
	if errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "\nstopped following; job %d keeps running (`buildyard cancel %d` stops it)\n", id, id)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "job %d %s in %s\n", job.ID, job.Status, duration(job))
	if job.Status != "succeeded" {
		return errJobFailed{job}
	}
	return nil
}
