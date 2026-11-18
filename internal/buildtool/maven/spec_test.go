package maven

import (
	"os"
	"strings"
	"testing"
)

func TestArgv(t *testing.T) {
	presets := MergePresets([]SkipPreset{
		{ID: "no-ui", Label: "UI", Args: []string{"-Ddremio.no-ui"}},
		{ID: "sources"}, // removed
	})
	cases := []struct {
		name string
		spec RunSpec
		want string
	}{
		{"goals only", RunSpec{Goals: []string{"clean", "install"}}, "mvn -B clean install"},
		{"everything", RunSpec{
			Goals:      []string{"install"},
			Modules:    Modules{Mode: "selected", List: []string{":core", "g:app", ":core"}, AlsoMake: true, AlsoMakeDependents: true},
			Profiles:   []string{"fast", "!slow"},
			Skips:      []string{"tests", "no-ui"},
			Properties: map[string]string{"b": "2", "-Da": "1", "flag": ""},
			Flags:      Flags{Offline: true, NoSnapshotUpdates: true, Threads: "1.5C", NoTransferProgress: true, FailMode: "end", Errors: true},
			LogLevel:   "quiet",
			ExtraArgs:  []string{"-Dmaven.repo.local=/r"},
		}, "mvn -B -o -nsu -T 1.5C -ntp -fae -e -q -pl :core,g:app -am -amd -P fast,!slow -DskipTests -Ddremio.no-ui -Da=1 -Db=2 -Dflag install -Dmaven.repo.local=/r"},
		{"warnings only", RunSpec{Goals: []string{"install"}, LogLevel: "warn"}, "mvn -B -Dorg.slf4j.simpleLogger.defaultLogLevel=warn install"},
		{"all modules ignores list", RunSpec{Goals: []string{"verify"}, Modules: Modules{Mode: "all", List: []string{":x"}, AlsoMake: true}, LogLevel: "debug", Flags: Flags{FailMode: "fast"}},
			"mvn -B -ff -X verify"},
	}
	for _, c := range cases {
		s := c.spec.Normalize()
		if err := s.Validate(presets); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := strings.Join(s.Argv("mvn", presets), " "); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	presets := DefaultSkipPresets()
	bad := []RunSpec{
		{},
		{Goals: []string{"-X"}},
		{Goals: []string{"install"}, Modules: Modules{Mode: "selected"}},
		{Goals: []string{"install"}, Modules: Modules{Mode: "selected", List: []string{"a b"}}},
		{Goals: []string{"install"}, Skips: []string{"sources-x"}},
		{Goals: []string{"install"}, Profiles: []string{"a b"}},
		{Goals: []string{"install"}, Properties: map[string]string{"a b": "1"}},
		{Goals: []string{"install"}, Flags: Flags{Threads: "lots"}},
		{Goals: []string{"install"}, Flags: Flags{FailMode: "maybe"}},
		{Goals: []string{"install"}, LogLevel: "trace"},
	}
	for i, s := range bad {
		if err := s.Normalize().Validate(presets); err == nil {
			t.Errorf("case %d should fail: %+v", i, s)
		}
	}
	if MergePresets(nil)[0].ID != "tests" || len(MergePresets([]SkipPreset{{ID: "tests"}})) != len(presets)-1 {
		t.Error("merge presets")
	}
	if ValidatePresets([]SkipPreset{{ID: "a"}, {ID: "a"}}) == nil || ValidatePresets([]SkipPreset{{ID: "a b"}}) == nil {
		t.Error("validate presets")
	}
}

func TestParseDiscovery(t *testing.T) {
	out, err := os.ReadFile("../../../testdata/maven-logs/07-active-profiles.log")
	if err != nil {
		t.Fatal(err)
	}
	mods := ParseDiscovery(out)
	var got []string
	for _, m := range mods {
		got = append(got, m.Key()+"|"+m.Packaging+"|"+m.Name+"|"+strings.Join(m.Profiles, ","))
	}
	want := []string{
		"demo:demo-parent|pom|Demo Parent|with-extra,ci",
		"demo:demo-core|jar|Demo Core|with-extra,ci",
		"demo:demo-app|jar|Demo App|with-extra,ci",
		"demo:demo-extra|pom|Demo Extra|with-extra,ci",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}

	out, _ = os.ReadFile("../../../testdata/maven-logs/08-active-profiles-none.log")
	if mods := ParseDiscovery(out); len(mods) != 3 || len(mods[1].Profiles) != 0 || mods[2].Name != "Demo App" {
		t.Fatalf("none: %+v", mods)
	}

	// Single module with thread-name prefixes: the name comes from the "Building" line.
	single := "[main] [INFO] Scanning for projects...\n" +
		"[main] [INFO] ---------------------------< demo:solo >---------------------------\n" +
		"[main] [INFO] Building Solo Thing 2.1\n" +
		"Active Profiles for Project 'demo:solo:jar:2.1':\n\nThe following profiles are active:\n\n - dev (source: demo:solo:2.1)\n"
	if mods := ParseDiscovery([]byte(single)); len(mods) != 1 || mods[0].Name != "Solo Thing" || mods[0].Profiles[0] != "dev" {
		t.Fatalf("single: %+v", mods)
	}
}

func TestDiscoverArgs(t *testing.T) {
	s := RunSpec{Goals: []string{"install"}, Profiles: []string{"x"}, Skips: []string{"tests"},
		Modules: Modules{Mode: "selected", List: []string{":a"}}, ExtraArgs: []string{"-q"}}.Normalize()
	got := strings.Join(DiscoverArgs(s, DefaultSkipPresets(), true, ExtArgs("/e.jar", "/ev")), " ")
	if got != "-B -o -P x -DskipTests -Dmaven.ext.class.path=/e.jar -Dbuildyard.events=/ev help:active-profiles" {
		t.Fatal(got)
	}
}

func TestReadSession(t *testing.T) {
	in := `{"t":"project_start","key":"a:b"}
not json
{"t":"session","ts":1,"root":"/r","projects":[{"key":"g:p","name":"P","packaging":"pom","dir":"/r","deps":[]},{"key":"g:c","name":"C","packaging":"jar","dir":"/r/c","aggregator":"g:p","parent":"g:p","deps":["g:p"]}]}
`
	s := ReadSession(strings.NewReader(in))
	if s == nil || s.Root != "/r" || len(s.Projects) != 2 || s.Projects[1].Aggregator != "g:p" || s.Projects[1].Deps[0] != "g:p" {
		t.Fatalf("%+v", s)
	}
	if ReadSession(strings.NewReader("{}\n")) != nil {
		t.Fatal("no session expected")
	}
}

func TestResources(t *testing.T) {
	presets := DefaultSkipPresets()
	defaults := Resources{Threads: "1C", HeapMB: 8192, TestForks: "2"}

	s := RunSpec{Goals: []string{"install"}}.Normalize().WithDefaults(defaults)
	if got := strings.Join(s.Argv("/x/mvnw", presets), " "); got != "/x/mvnw -B -T 1C -DforkCount=2 install" {
		t.Errorf("project defaults: %s", got)
	}
	if opts, ok := s.MavenOpts("-Xss4m -Xmx2g"); !ok || opts != "-Xss4m -Xmx8g" {
		t.Errorf("MAVEN_OPTS: %q %v", opts, ok)
	}
	if got := strings.Join(s.Argv("/usr/bin/mvnd", presets), " "); !strings.Contains(got, "-Dmvnd.maxHeapSize=8g") {
		t.Errorf("mvnd heap: %s", got)
	}

	// The run overrides the defaults; "1", -1 and "none" mean Maven's own behaviour.
	s = RunSpec{Goals: []string{"install"}, Flags: Flags{Threads: "1"}, Resources: Resources{HeapMB: -1, TestForks: "none"}}.
		Normalize().WithDefaults(defaults)
	if got := strings.Join(s.Argv("mvn", presets), " "); got != "mvn -B install" {
		t.Errorf("explicit Maven defaults: %s", got)
	}
	if got := strings.Join(s.Argv("/usr/bin/mvnd", presets), " "); got != "/usr/bin/mvnd -B -T 1 install" {
		t.Errorf("mvnd is parallel by default, so 1 thread must be explicit: %s", got)
	}
	if _, ok := s.MavenOpts("-Xmx2g"); ok {
		t.Error("HeapMB -1 must leave MAVEN_OPTS alone")
	}
	s = RunSpec{Goals: []string{"install"}, Resources: Resources{HeapMB: 1536, TestForks: "0.5C"},
		Properties: map[string]string{"forkCount": "3"}}.Normalize()
	if opts, _ := s.MavenOpts(""); opts != "-Xmx1536m" {
		t.Errorf("heap in MB: %q", opts)
	}
	if got := strings.Join(s.Argv("mvn", presets), " "); got != "mvn -B -DforkCount=3 install" {
		t.Errorf("an explicit -DforkCount wins: %s", got)
	}

	for _, r := range []Resources{{HeapMB: -2}, {TestForks: "many"}, {Threads: "x"}} {
		if ValidateResources(r) == nil {
			t.Errorf("%+v should be invalid", r)
		}
	}
	if (RunSpec{Goals: []string{"install"}, Resources: Resources{TestForks: "lots"}}).Normalize().Validate(presets) == nil {
		t.Error("bad testForks in a run should be invalid")
	}
}

func TestWithout(t *testing.T) {
	presets := DefaultSkipPresets()
	all := RunSpec{Goals: []string{"install"}}.Normalize().Without([]string{":a", "g:b"})
	if got := strings.Join(all.Argv("mvn", presets), " "); got != "mvn -B -pl !:a,!g:b install" {
		t.Errorf("all modules: %s", got)
	}
	sel := RunSpec{Goals: []string{"install"}, Modules: Modules{Mode: "selected", List: []string{":a", ":c", "!:d"}, AlsoMake: true}}.Normalize()
	if got := strings.Join(sel.Without([]string{":a", ":d"}).Argv("mvn", presets), " "); got != "mvn -B -pl :c,!:a,!:d -am install" {
		t.Errorf("selected: %s", got)
	}
	if err := all.Normalize().Validate(presets); err != nil {
		t.Errorf("validate: %v", err)
	}
}
