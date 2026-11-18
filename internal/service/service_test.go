package service

import (
	"strings"
	"testing"
)

var spec = Spec{
	Executable: "/Users/me/My Tools/buildyard",
	Args:       []string{"serve", "--service"},
	Env:        map[string]string{"PATH": "/usr/bin:/opt/a&b"},
	LogPath:    "/tmp/daemon.log",
}

func TestLaunchdRender(t *testing.T) {
	b, err := (&Launchd{}).Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/Users/me/My Tools/buildyard</string>",
		"<string>serve</string>",
		"<string>--service</string>",
		"<key>StandardErrorPath</key>",
		"<string>/usr/bin:/opt/a&amp;b</string>", // escaped for XML
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q:\n%s", want, out)
		}
	}
}

func TestSystemdRender(t *testing.T) {
	b, err := (&Systemd{}).Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		`ExecStart="/Users/me/My Tools/buildyard" "serve" "--service"`,
		"StandardError=append:/tmp/daemon.log",
		`Environment="PATH=/usr/bin:/opt/a&b"`,
		"WantedBy=default.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit missing %q:\n%s", want, out)
		}
	}
}
