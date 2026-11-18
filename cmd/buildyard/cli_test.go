package main

import (
	"testing"

	"github.com/binarycat0/buildyard/internal/client"
)

func TestPickProject(t *testing.T) {
	ps := []client.Project{
		{ID: 1, Name: "Dremio", RootPath: "/src/dremio"},
		{ID: 2, Name: "dremio-oss", RootPath: "/src/dremio/oss/"},
		{ID: 3, Name: "other", RootPath: "/src/dremio-other"},
	}
	cases := []struct {
		arg, cwd string
		want     int64
	}{
		{"2", "/", 2},
		{"dremio", "/", 1},            // name, case-insensitive
		{"", "/src/dremio/sabot", 1},  // inside the project
		{"", "/src/dremio/oss/x", 2},  // deepest project wins
		{"", "/src/dremio-other", 3},  // not confused by a shared prefix
		{"", "/src/dremio/oss", 2},    // the root itself
		{"", "/home", 0},              // outside every project
		{"missing", "/src/dremio", 0}, // explicit name that does not exist
	}
	for _, c := range cases {
		p, err := pickProject(ps, c.arg, c.cwd)
		if c.want == 0 {
			if err == nil {
				t.Errorf("pickProject(%q, %q) = %d, want an error", c.arg, c.cwd, p.ID)
			}
			continue
		}
		if err != nil || p.ID != c.want {
			t.Errorf("pickProject(%q, %q) = %d, %v; want %d", c.arg, c.cwd, p.ID, err, c.want)
		}
	}
}

func TestPickTask(t *testing.T) {
	ts := []client.Task{{ID: 7, Name: "Quick build"}, {ID: 8, Name: "7"}}
	if got, err := pickTask(ts, "quick BUILD"); err != nil || got.ID != 7 {
		t.Errorf("by name: %v %v", got, err)
	}
	if got, err := pickTask(ts, "7"); err != nil || got.ID != 7 {
		t.Errorf("an id wins over a name: %v %v", got, err)
	}
	if _, err := pickTask(ts, "nope"); err == nil {
		t.Error("want an error for an unknown task")
	}
}
