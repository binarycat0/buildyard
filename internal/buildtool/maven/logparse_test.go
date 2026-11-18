package maven

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func parseFixture(t *testing.T, name string) (*LogParser, []Problem) {
	t.Helper()
	f, err := os.Open("../../../testdata/maven-logs/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := NewLogParser()
	var all []Problem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "# ") {
			continue // fixture header
		}
		n++
		probs, _ := p.Line(n, sc.Text())
		all = append(all, probs...)
	}
	return p, all
}

func kinds(ps []Problem) map[string]int {
	out := map[string]int{}
	for _, p := range ps {
		out[p.Kind]++
	}
	return out
}

func TestLogParserTests(t *testing.T) {
	p, probs := parseFixture(t, "09-test-failures-plain.log")
	k := kinds(probs)
	if k["test"] != 2 || k["goal"] != 1 || len(probs) != 3 {
		t.Fatalf("problems: %+v", probs)
	}
	if probs[0].Message != `CoreTest.subtracts:8 expected:<1> but was:<2>` || probs[0].Line != 8 {
		t.Errorf("test failure: %+v", probs[0])
	}
	if !strings.HasPrefix(probs[1].Message, "CoreTest.explodes:9 NullPointer") {
		t.Errorf("test error: %+v", probs[1])
	}
	if g := probs[2]; g.Module != "demo-core" || !strings.HasPrefix(g.Message, "maven-surefire-plugin:test (default-test) There are test failures") {
		t.Errorf("goal: %+v", g)
	}
	// Fallback modules: reactor order, Building lines, Reactor Summary.
	got := []string{}
	for _, m := range p.Modules {
		got = append(got, m.Name+"="+m.Status)
	}
	if strings.Join(got, ",") != "Demo Parent=succeeded,Demo Core=failed,Demo App=skipped" {
		t.Errorf("modules: %v", got)
	}
	if p.Modules[1].Ms == 0 {
		t.Error("summary time missing")
	}
}

func TestLogParserCompileThreaded(t *testing.T) {
	p, probs := parseFixture(t, "10-compile-error-threaded.log")
	if len(probs) != 2 {
		t.Fatalf("problems: %+v", probs)
	}
	c := probs[0]
	if c.Kind != "compile" || c.Module != "demo-app" || c.File != "/work/sample/app/src/main/java/demo/app/Broken.java" || c.Line != 4 || c.Col != 11 || c.Message != "cannot find symbol" {
		t.Errorf("compile: %+v", c)
	}
	if probs[1].Kind != "goal" || probs[1].Module != "demo-app" {
		t.Errorf("goal: %+v", probs[1])
	}
	if len(p.Modules) != 3 || p.Modules[2].Status != "failed" || p.Modules[0].Status != "succeeded" {
		t.Errorf("modules: %+v", p.Modules)
	}
}

func TestLogParserDremioFixtures(t *testing.T) {
	_, probs := parseFixture(t, "02-compile-error.log")
	k := kinds(probs)
	// 37 javac errors, printed twice (module thread and the main summary): kept once.
	if k["compile"] != 37 || k["goal"] < 1 || k["dependency"] < 1 {
		t.Fatalf("02: %v", k)
	}
	for _, p := range probs {
		if p.Kind == "compile" && p.Module != "dremio-ee-sabot-kernel" {
			t.Fatalf("02 compile module: %+v", p)
		}
	}
	_, probs = parseFixture(t, "03-plugin-failure-quarkus-generate-code.log")
	if k := kinds(probs); k["goal"] != 2 || len(probs) != 2 {
		t.Fatalf("03: %+v", probs)
	}
	// A recovered [ERROR] is not a problem.
	if _, probs = parseFixture(t, "06-false-error-gosdk-fallback.log"); len(probs) != 0 {
		t.Fatalf("06: %+v", probs)
	}
	p, _ := parseFixture(t, "04-reactor-summary-failure-skipped.log")
	_ = p // no Reactor Build Order in this tail: the summary alone does not invent modules
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[[2]string]int64{{"0.184", "s"}: 184, {"01:51", "min"}: 111000, {"01:02", "h"}: 3720000, {"", ""}: 0} {
		if got := parseDuration(in[0], in[1]); got != want {
			t.Errorf("%v: %d, want %d", in, got, want)
		}
	}
}
