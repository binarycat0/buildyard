package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestFollowResumes checks that a stream closed before "end" is resumed with
// Last-Event-ID and that no line is printed twice.
func TestFollowResumes(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			if r.URL.Query().Get("from") != "1" {
				t.Errorf("first request: from = %q, want 1", r.URL.Query().Get("from"))
			}
			for n := 1; n <= 3; n++ {
				fmt.Fprintf(w, "id: %d\nevent: log\ndata: {\"type\":\"line\",\"n\":%d,\"text\":\"line %d\"}\n\n", n, n, n)
			}
			fmt.Fprint(w, "event: status\ndata: {\"type\":\"status\",\"status\":\"running\"}\n\n")
			// closed without "end", as when the daemon drops a slow reader
		default:
			if got := r.Header.Get("Last-Event-ID"); got != "3" {
				t.Errorf("resume: Last-Event-ID = %q, want 3", got)
			}
			fmt.Fprint(w, ": ping\n\n")
			fmt.Fprint(w, "id: 3\nevent: log\ndata: {\"n\":3,\"text\":\"line 3\"}\n\n") // duplicate
			fmt.Fprint(w, "id: 4\nevent: log\ndata: {\"n\":4,\"text\":\"line 4\"}\n\n")
			fmt.Fprint(w, "event: end\ndata: {\"id\":9,\"status\":\"failed\"}\n\n")
		}
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL, HTTP: srv.Client()}
	var got []string
	job, err := c.Follow(context.Background(), 9, 1, func(n int, text string) {
		got = append(got, strconv.Itoa(n)+":"+text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != 9 || job.Status != "failed" || !job.Finished() {
		t.Errorf("job = %+v", job)
	}
	want := []string{"1:line 1", "2:line 2", "3:line 3", "4:line 4"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
}

func TestAPIErrorAndDaemonDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad_request","message":"unknown task for this project"}`)
	}))
	c := &Client{Base: srv.URL, HTTP: srv.Client()}
	_, err := c.RunTask(context.Background(), 1, 2)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "unknown task for this project" {
		t.Errorf("err = %v", err)
	}
	srv.Close()

	// A port nobody listens on.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if _, err := New(port).Health(context.Background()); !errors.Is(err, ErrDaemonDown) {
		t.Errorf("err = %v, want ErrDaemonDown", err)
	}
}
