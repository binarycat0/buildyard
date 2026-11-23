// Package telemetry exports builds to an OpenTelemetry backend: each job becomes a
// trace (startup, modules, goals, downloads) and its output becomes log records,
// both sent as OTLP/HTTP JSON to a collector such as grafana/otel-lgtm.
//
// Export is best effort and never slows a build down: records wait in bounded
// in-memory queues, a background loop sends them in batches, and when the backend
// is down or too slow the oldest batches are dropped and counted. The job's log
// file stays the source of truth for the web UI.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/binarycat0/buildyard/internal/config"
)

const (
	flushEvery = time.Second
	batchSize  = 2000    // records per request
	maxQueued  = 200_000 // per signal; beyond this the oldest records are dropped
	sendTries  = 3
)

// Exporter queues spans and log records and sends them in the background.
// Create it with New and start it with Run.
type Exporter struct {
	log     *slog.Logger
	version string
	client  *http.Client

	mu     sync.Mutex
	cfg    config.Telemetry
	spans  []span
	logs   []logRecord
	kick   chan struct{}
	status Status
}

// Status is what the Settings page shows about the export.
type Status struct {
	SentSpans    int64      `json:"sentSpans"`
	SentLogs     int64      `json:"sentLogs"`
	DroppedSpans int64      `json:"droppedSpans"`
	DroppedLogs  int64      `json:"droppedLogs"`
	QueuedSpans  int        `json:"queuedSpans"`
	QueuedLogs   int        `json:"queuedLogs"`
	LastOK       *time.Time `json:"lastOk,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	LastErrorAt  *time.Time `json:"lastErrorAt,omitempty"`
}

// New returns an exporter with the given settings; version is Buildyard's own.
func New(cfg config.Telemetry, version string, log *slog.Logger) *Exporter {
	if log == nil {
		log = slog.Default()
	}
	return &Exporter{log: log, version: version, cfg: cfg, kick: make(chan struct{}, 1),
		client: &http.Client{Timeout: 10 * time.Second}}
}

// Configure applies new settings. Jobs already running keep exporting with the
// signals they started with; turning export off drops what is still queued.
func (e *Exporter) Configure(cfg config.Telemetry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
	if !cfg.Enabled {
		e.spans, e.logs = nil, nil
	}
}

// Settings returns the current settings.
func (e *Exporter) Settings() config.Telemetry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// Status returns counters and the last export result.
func (e *Exporter) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.status
	s.QueuedSpans, s.QueuedLogs = len(e.spans), len(e.logs)
	return s
}

// Run sends queued records until ctx ends, then makes a last attempt to send the rest.
func (e *Exporter) Run(ctx context.Context) {
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			last, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			e.flush(last)
			cancel()
			return
		case <-tick.C:
		case <-e.kick:
		}
		e.flush(ctx)
	}
}

func (e *Exporter) addSpan(s span) {
	e.mu.Lock()
	if !e.cfg.Enabled {
		e.mu.Unlock()
		return
	}
	e.spans = append(e.spans, s)
	if over := len(e.spans) - maxQueued; over > 0 {
		e.spans = append(e.spans[:0:0], e.spans[over:]...)
		e.status.DroppedSpans += int64(over)
	}
	full := len(e.spans) >= batchSize
	e.mu.Unlock()
	if full {
		e.wake()
	}
}

func (e *Exporter) addLog(r logRecord) {
	e.mu.Lock()
	if !e.cfg.Enabled {
		e.mu.Unlock()
		return
	}
	e.logs = append(e.logs, r)
	if over := len(e.logs) - maxQueued; over > 0 {
		e.logs = append(e.logs[:0:0], e.logs[over:]...)
		e.status.DroppedLogs += int64(over)
	}
	full := len(e.logs) >= batchSize
	e.mu.Unlock()
	if full {
		e.wake()
	}
}

func (e *Exporter) wake() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// flush sends everything queued, batch by batch. A batch that cannot be sent is
// dropped after a few tries, so a dead backend costs memory only up to maxQueued.
func (e *Exporter) flush(ctx context.Context) {
	for ctx.Err() == nil {
		e.mu.Lock()
		cfg := e.cfg
		var spans []span
		var logs []logRecord
		if n := min(len(e.spans), batchSize); n > 0 {
			spans, e.spans = e.spans[:n:n], e.spans[n:]
		}
		if n := min(len(e.logs), batchSize); n > 0 {
			logs, e.logs = e.logs[:n:n], e.logs[n:]
		}
		e.mu.Unlock()
		if len(spans) == 0 && len(logs) == 0 {
			return
		}
		if len(spans) > 0 {
			err := e.send(ctx, cfg, "/v1/traces", e.tracesPayload(cfg, spans))
			e.record(err, len(spans), 0)
		}
		if len(logs) > 0 {
			err := e.send(ctx, cfg, "/v1/logs", e.logsPayload(cfg, logs))
			e.record(err, 0, len(logs))
		}
	}
}

func (e *Exporter) record(err error, spans, logs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	if err != nil {
		if e.status.LastError == "" || e.status.LastErrorAt == nil || now.Sub(*e.status.LastErrorAt) > time.Minute {
			e.log.Warn("telemetry export failed", "err", err)
		}
		e.status.LastError, e.status.LastErrorAt = err.Error(), &now
		e.status.DroppedSpans += int64(spans)
		e.status.DroppedLogs += int64(logs)
		return
	}
	e.status.LastOK = &now
	e.status.SentSpans += int64(spans)
	e.status.SentLogs += int64(logs)
}

// send posts one OTLP/HTTP JSON request, retrying network errors, 429 and 5xx.
func (e *Exporter) send(ctx context.Context, cfg config.Telemetry, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var last error
	for try := 0; try < sendTries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(try) * 500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "buildyard/"+e.version)
		for k, v := range cfg.Headers {
			req.Header.Set(k, v)
		}
		resp, err := e.client.Do(req)
		if err != nil {
			last = err
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		last = fmt.Errorf("%s: %s %s", path, resp.Status, bytes.TrimSpace(msg))
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return last // a 4xx other than 429 will not get better on retry
		}
	}
	return last
}

// Test sends one span and one log record straight away (not through the queues)
// and reports whether the backend took them.
func (e *Exporter) Test(ctx context.Context, cfg config.Telemetry) error {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if cfg.ServiceName == "" {
		cfg.ServiceName = config.DefaultTelemetry().ServiceName
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.Endpoint == "" {
		return errors.New("telemetry endpoint is required")
	}
	now := time.Now()
	tid, sid := newTraceID(), newSpanID()
	s := span{TraceID: tid, SpanID: sid, Name: "buildyard connection test", Kind: 1,
		Start: now.Add(-time.Millisecond), End: now, Attrs: []kv{str("buildyard.test", "true")}}
	r := logRecord{Time: now, Severity: sevInfo, Body: "Buildyard connection test", TraceID: tid, SpanID: sid,
		Attrs: []kv{str("buildyard.test", "true")}}
	var errs []error
	if cfg.Traces {
		errs = append(errs, e.send(ctx, cfg, "/v1/traces", e.tracesPayload(cfg, []span{s})))
	}
	if cfg.Logs {
		errs = append(errs, e.send(ctx, cfg, "/v1/logs", e.logsPayload(cfg, []logRecord{r})))
	}
	return errors.Join(errs...)
}

// ---- OTLP JSON ----

// kv is an OTLP attribute. Exactly one value field is set.
type kv struct {
	Key   string `json:"key"`
	Value anyVal `json:"value"`
}

type anyVal struct {
	S *string `json:"stringValue,omitempty"`
	I *string `json:"intValue,omitempty"` // int64 as a decimal string, per the OTLP JSON mapping
	B *bool   `json:"boolValue,omitempty"`
}

func str(k, v string) kv { return kv{Key: k, Value: anyVal{S: &v}} }
func num(k string, v int64) kv {
	s := fmt.Sprint(v)
	return kv{Key: k, Value: anyVal{I: &s}}
}
func flag(k string, v bool) kv { return kv{Key: k, Value: anyVal{B: &v}} }

type span struct {
	TraceID, SpanID, ParentID string
	Name                      string
	Kind                      int
	Start, End                time.Time
	Attrs                     []kv
	Error                     string // non-empty: status ERROR with this message
}

const sevInfo = 9

type logRecord struct {
	Time            time.Time
	Severity        int
	Body            string
	TraceID, SpanID string
	Attrs           []kv
}

func nanos(t time.Time) string { return fmt.Sprint(t.UnixNano()) }

func (e *Exporter) resource(cfg config.Telemetry) map[string]any {
	host, _ := os.Hostname()
	attrs := []kv{
		str("service.name", cfg.ServiceName),
		str("service.version", e.version),
		str("host.name", host),
		str("os.type", runtime.GOOS),
		str("host.arch", runtime.GOARCH),
	}
	return map[string]any{"attributes": attrs}
}

func (e *Exporter) scope() map[string]any {
	return map[string]any{"name": "buildyard", "version": e.version}
}

func (e *Exporter) tracesPayload(cfg config.Telemetry, spans []span) any {
	out := make([]map[string]any, len(spans))
	for i, s := range spans {
		m := map[string]any{
			"traceId":           s.TraceID,
			"spanId":            s.SpanID,
			"name":              s.Name,
			"kind":              s.Kind,
			"startTimeUnixNano": nanos(s.Start),
			"endTimeUnixNano":   nanos(s.End),
			"attributes":        s.Attrs,
		}
		if s.ParentID != "" {
			m["parentSpanId"] = s.ParentID
		}
		if s.Error != "" {
			m["status"] = map[string]any{"code": 2, "message": s.Error}
		}
		out[i] = m
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   e.resource(cfg),
		"scopeSpans": []any{map[string]any{"scope": e.scope(), "spans": out}},
	}}}
}

var sevText = map[int]string{1: "TRACE", 5: "DEBUG", sevInfo: "INFO", 13: "WARN", 17: "ERROR"}

func (e *Exporter) logsPayload(cfg config.Telemetry, logs []logRecord) any {
	out := make([]map[string]any, len(logs))
	for i, r := range logs {
		m := map[string]any{
			"timeUnixNano":         nanos(r.Time),
			"observedTimeUnixNano": nanos(r.Time),
			"body":                 anyVal{S: &logs[i].Body},
			"attributes":           r.Attrs,
		}
		if r.Severity != 0 { // raw output lines have none
			m["severityNumber"], m["severityText"] = r.Severity, sevText[r.Severity]
		}
		if r.TraceID != "" {
			m["traceId"], m["spanId"] = r.TraceID, r.SpanID
		}
		out[i] = m
	}
	return map[string]any{"resourceLogs": []any{map[string]any{
		"resource":  e.resource(cfg),
		"scopeLogs": []any{map[string]any{"scope": e.scope(), "logRecords": out}},
	}}}
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newTraceID() string { return randHex(16) }
func newSpanID() string  { return randHex(8) }
