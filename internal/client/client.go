// Package client talks to a running Buildyard daemon over its HTTP API. The
// CLI commands (status, run, jobs, logs, ...) use it.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrDaemonDown means nothing is listening on the daemon's address.
var ErrDaemonDown = errors.New("buildyard daemon is not running")

// Client is a typed client for the daemon API.
type Client struct {
	Base string       // e.g. http://127.0.0.1:7777
	HTTP *http.Client // for plain calls; streams use a client without a timeout
}

// New returns a client for the daemon on 127.0.0.1:port.
func New(port int) *Client {
	return &Client{
		Base: "http://127.0.0.1:" + strconv.Itoa(port),
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
}

// APIError is an error the daemon returned.
type APIError struct {
	Status  int
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("daemon returned %d", e.Status)
}

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return ErrDaemonDown
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		e := &APIError{Status: resp.StatusCode}
		_ = json.NewDecoder(resp.Body).Decode(e)
		return e
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, c.HTTP, http.MethodGet, path, nil, out)
}

// Health is GET /api/health.
type Health struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	DataDir   string `json:"dataDir"`
	UptimeSec int    `json:"uptimeSec"`
}

// JobSummary is the short job form on projects and tasks.
type JobSummary struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"`
	QueuedAt   time.Time  `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// Project is one entry of GET /api/projects.
type Project struct {
	ID       int64       `json:"id"`
	Name     string      `json:"name"`
	RootPath string      `json:"rootPath"`
	LastJob  *JobSummary `json:"lastJob"`
}

// Task is a saved run (run profile).
type Task struct {
	ID      int64       `json:"id"`
	Name    string      `json:"name"`
	Pinned  bool        `json:"pinned"`
	LastJob *JobSummary `json:"lastJob"`
}

// Job is GET /api/jobs/{id}.
type Job struct {
	ID         int64      `json:"id"`
	ProjectID  int64      `json:"projectId"`
	ProfileID  *int64     `json:"profileId"`
	Argv       []string   `json:"argv"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exitCode"`
	QueuedAt   time.Time  `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	LiveLines  int        `json:"liveLines"`
}

// Finished reports whether the job reached a final status.
func (j Job) Finished() bool { return j.Status != "queued" && j.Status != "running" }

func (c *Client) Health(ctx context.Context) (h Health, err error) {
	return h, c.get(ctx, "/api/health", &h)
}

func (c *Client) Projects(ctx context.Context) (ps []Project, err error) {
	return ps, c.get(ctx, "/api/projects", &ps)
}

func (c *Client) Tasks(ctx context.Context, projectID int64) (ts []Task, err error) {
	return ts, c.get(ctx, fmt.Sprintf("/api/projects/%d/profiles", projectID), &ts)
}

func (c *Client) Jobs(ctx context.Context, projectID int64, limit int) (js []Job, err error) {
	return js, c.get(ctx, fmt.Sprintf("/api/projects/%d/jobs?limit=%d", projectID, limit), &js)
}

func (c *Client) Job(ctx context.Context, id int64) (j Job, err error) {
	return j, c.get(ctx, fmt.Sprintf("/api/jobs/%d", id), &j)
}

// RunTask queues a saved task.
func (c *Client) RunTask(ctx context.Context, projectID, taskID int64) (j Job, err error) {
	in := map[string]any{"profileId": taskID}
	return j, c.do(ctx, c.HTTP, http.MethodPost, fmt.Sprintf("/api/projects/%d/jobs", projectID), in, &j)
}

// Rerun queues a job with the same spec as an earlier one.
func (c *Client) Rerun(ctx context.Context, jobID int64) (j Job, err error) {
	return j, c.do(ctx, c.HTTP, http.MethodPost, fmt.Sprintf("/api/jobs/%d/rerun", jobID), nil, &j)
}

// Cancel stops a job (graceful, then killed after a grace period).
func (c *Client) Cancel(ctx context.Context, jobID int64) error {
	return c.do(ctx, c.HTTP, http.MethodPost, fmt.Sprintf("/api/jobs/%d/cancel", jobID), nil, nil)
}

// LogPage is a slice of a job's log.
type LogPage struct {
	From  int      `json:"from"`
	Lines []string `json:"lines"`
	Total int      `json:"total"`
}

func (c *Client) Log(ctx context.Context, jobID int64, from, limit int) (p LogPage, err error) {
	q := url.Values{"from": {strconv.Itoa(from)}, "limit": {strconv.Itoa(limit)}}
	return p, c.get(ctx, fmt.Sprintf("/api/jobs/%d/log?%s", jobID, q.Encode()), &p)
}

// Follow streams a job's log from line from (0 = the last 2000 lines), calling
// line for each line, until the job ends or ctx is cancelled. It returns the
// finished job. A dropped connection is resumed after the last line seen.
func (c *Client) Follow(ctx context.Context, jobID int64, from int, line func(n int, text string)) (Job, error) {
	stream := &http.Client{} // no timeout: builds run for hours
	last := from - 1
	for {
		u := fmt.Sprintf("%s/api/jobs/%d/stream", c.Base, jobID)
		if last < 1 && from > 0 {
			u += "?from=" + strconv.Itoa(from)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return Job{}, err
		}
		if last >= 1 {
			req.Header.Set("Last-Event-ID", strconv.Itoa(last))
		}
		resp, err := stream.Do(req)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				return Job{}, ErrDaemonDown
			}
			return Job{}, err
		}
		if resp.StatusCode != http.StatusOK {
			e := &APIError{Status: resp.StatusCode}
			_ = json.NewDecoder(resp.Body).Decode(e)
			resp.Body.Close()
			return Job{}, e
		}
		end, err := readEvents(resp.Body, func(event, data string) {
			if event != "log" {
				return
			}
			var ev struct {
				N    int    `json:"n"`
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil && ev.N > last {
				last = ev.N
				line(ev.N, ev.Text)
			}
		})
		resp.Body.Close()
		if end != nil {
			return *end, nil
		}
		if ctx.Err() != nil {
			return Job{}, ctx.Err()
		}
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return Job{}, err
		}
		// The stream closed without "end" (e.g. a slow reader was dropped): resume.
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// readEvents parses server-sent events until the "end" event, whose data
// (the finished job) it returns, or until the stream ends.
func readEvents(r io.Reader, on func(event, data string)) (*Job, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var event string
	var data []string
	for sc.Scan() {
		l := sc.Text()
		switch {
		case l == "":
			if event == "end" {
				var j Job
				if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &j); err != nil {
					return nil, err
				}
				return &j, nil
			}
			if event != "" || len(data) > 0 {
				on(event, strings.Join(data, "\n"))
			}
			event, data = "", nil
		case strings.HasPrefix(l, ":"):
		case strings.HasPrefix(l, "event:"):
			event = strings.TrimSpace(l[len("event:"):])
		case strings.HasPrefix(l, "data:"):
			data = append(data, strings.TrimPrefix(l[len("data:"):], " "))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}
