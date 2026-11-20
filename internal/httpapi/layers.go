package httpapi

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/binarycat0/buildyard/internal/buildtool/maven"
	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/toolconfig"
)

// layerRow is one setting in one layer of a project's configuration.
type layerRow struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`           // empty = not set
	Source string   `json:"source"`           // file path, "Buildyard" or "environment"
	Edit   string   `json:"edit,omitempty"`   // UI link where it is changed
	Note   string   `json:"note,omitempty"`   // e.g. "overridden by the project"
	Status string   `json:"status,omitempty"` // "" | used | overridden | unused
}

// layer is a group of settings: the user's global ones, the repository's, the project's.
type layer struct {
	ID    string     `json:"id"` // global | repo | project
	Title string     `json:"title"`
	Hint  string     `json:"hint"`
	Rows  []layerRow `json:"rows"`
}

// projectLayers is what a project's runs are configured by, from the widest layer to the
// narrowest. A task's own flags come last and win; the UI adds them.
type projectLayers struct {
	Tool   string  `json:"tool"`
	Layers []layer `json:"layers"`
	Order  string  `json:"order"` // how the layers combine, for this tool
}

func (s *Server) projectLayers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	out, err := s.layersFor(r.Context(), p)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) layersFor(ctx context.Context, p store.Project) (projectLayers, error) {
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		return projectLayers{}, err
	}
	info, err := s.runtimeFor(ctx, p)
	if err != nil {
		return projectLayers{}, err
	}
	jdkName := func(id *int64) string {
		if id == nil {
			return ""
		}
		if j, err := s.Store.GetJDK(ctx, *id); err == nil {
			return jdkText(j)
		}
		return ""
	}
	tool := settings.Tool(p.BuildTool)
	mvnd := info.Executable != nil && info.Executable.Kind == maven.ModeMvnd
	projectExe := p.Executable != nil
	projectJDK := p.JdkID != nil || p.ToolJdkID != nil

	// Global: Buildyard's defaults for the tool, then the user's own config files.
	global := layer{ID: "global", Title: "Global", Hint: "Your user: Buildyard's Maven defaults and the files in your home directory. Shared by every project."}
	global.Rows = append(global.Rows,
		layerRow{Name: "Launcher", Values: []string{execName(tool.Executable)}, Source: "Buildyard", Edit: "#/settings/maven",
			Status: pick(projectExe, "overridden", "used")},
		layerRow{Name: "JDK", Values: nonEmpty(orText(jdkName(tool.JdkID), "daemon JAVA_HOME")), Source: "Buildyard", Edit: "#/settings/maven",
			Status: pick(projectJDK, "overridden", "used")},
	)
	cat := s.toolConfigs()
	for _, id := range []string{"maven.settings", "maven.toolchains", "maven.mavenrc", "mvnd.properties"} {
		f, err := cat.Get(id)
		if err != nil {
			continue
		}
		row := layerRow{Name: f.Name, Source: f.Path, Edit: "#/settings/" + f.Tool, Note: "no such file"}
		if f.Exists {
			row.Note = ""
			row.Values = summarize(cat, f)
			if len(row.Values) == 0 {
				row.Values = []string{"present"}
			}
			row.Status = "used"
		}
		if f.Tool == "mvnd" {
			row.Note = strings.TrimPrefix(row.Note+"; read by mvnd only", "; ")
			if !mvnd && f.Exists {
				row.Status = "unused"
			}
		}
		global.Rows = append(global.Rows, row)
	}
	for _, k := range []string{"MAVEN_OPTS", "MAVEN_ARGS"} {
		if v := s.getenv(k); v != "" {
			row := layerRow{Name: k, Values: []string{v}, Source: "daemon environment", Status: "used"}
			if _, own := p.Env[k]; own {
				row.Status, row.Note = "overridden", "the project sets "+k
			}
			global.Rows = append(global.Rows, row)
		}
	}

	// Repository: what the project keeps in git; every Maven run there reads it.
	repo := layer{ID: "repo", Title: "Repository", Hint: "Files in the project's own repository. Every Maven run in it reads them, inside Buildyard or not."}
	if info.Wrapper != nil {
		v := "./mvnw"
		if info.Wrapper.MavenVersion != "" {
			v += " (Maven " + info.Wrapper.MavenVersion + ")"
		}
		repo.Rows = append(repo.Rows, layerRow{Name: "Maven wrapper", Values: []string{v}, Source: info.Wrapper.Path,
			Status: pick(info.Executable != nil && info.Executable.Kind == maven.ModeWrapper, "used", "unused")})
	}
	rc := maven.ReadRepoConfig(p.RootPath)
	for _, f := range []struct {
		name, path string
		lines      []string
		note       string
	}{
		{".mvn/maven.config", rc.MavenConfigPath, rc.MavenConfig, "added to every Maven command line"},
		{".mvn/jvm.config", rc.JvmConfigPath, rc.JvmConfig, "JVM options of Maven itself"},
		{".mvn/extensions.xml", rc.ExtensionsPath, rc.Extensions, "build extensions"},
	} {
		if f.path == "" {
			continue
		}
		vals := f.lines
		if len(vals) == 0 {
			vals = []string{"empty"}
		}
		repo.Rows = append(repo.Rows, layerRow{Name: f.name, Values: vals, Source: f.path, Note: f.note, Status: "used"})
	}

	// Project: what Buildyard keeps for this project.
	proj := layer{ID: "project", Title: "Project", Hint: "This project's settings in Buildyard. They override the global ones; every task of the project gets them."}
	exeRow := layerRow{Name: "Launcher", Source: "Buildyard", Edit: "settings:tool"}
	if projectExe {
		exeRow.Values, exeRow.Status = []string{execName(*p.Executable)}, "used"
	}
	if info.Executable != nil {
		exeRow.Note = "runs " + info.Executable.Path + " (" + info.Executable.Reason + ")"
	} else if info.ExecutableError != "" {
		exeRow.Note = info.ExecutableError
	}
	jdkRow := layerRow{Name: "JDK", Source: "Buildyard", Edit: "settings:tool"}
	if projectJDK {
		id := p.JdkID
		if p.ToolJdkID != nil {
			id = p.ToolJdkID
		}
		jdkRow.Values, jdkRow.Status = nonEmpty(jdkName(id)), "used"
	}
	proj.Rows = append(proj.Rows, exeRow, jdkRow)
	if len(p.Env) > 0 {
		keys := make([]string, 0, len(p.Env))
		for k := range p.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make([]string, len(keys))
		for i, k := range keys {
			vals[i] = k + "=" + p.Env[k]
		}
		proj.Rows = append(proj.Rows, layerRow{Name: "Environment", Values: vals, Source: "Buildyard", Edit: "settings:general", Status: "used"})
	}
	defs := taskDefaultsFor(p)
	presets := presetsFor(p)
	if len(defs.Profiles) > 0 {
		proj.Rows = append(proj.Rows, layerRow{Name: "Profiles", Values: []string{"-P " + strings.Join(defs.Profiles, ",")}, Source: "Buildyard", Edit: "settings:defaults", Status: "used"})
	}
	if len(defs.Properties) > 0 {
		args := maven.TaskDefaults{Properties: defs.Properties}.DefaultArgs(presets)
		proj.Rows = append(proj.Rows, layerRow{Name: "Properties", Values: args, Source: "Buildyard", Edit: "settings:defaults", Status: "used"})
	}
	if len(defs.Skips) > 0 {
		var labels []string
		for _, id := range defs.Skips {
			for _, sp := range presets {
				if sp.ID == id {
					labels = append(labels, sp.Label+" ("+strings.Join(sp.Args, " ")+")")
				}
			}
		}
		proj.Rows = append(proj.Rows, layerRow{Name: "Skips", Values: labels, Source: "Buildyard", Edit: "settings:defaults", Status: "used"})
	}
	res := resourcesFor(p)
	var resVals []string
	if res.Threads != "" {
		resVals = append(resVals, "-T "+res.Threads)
	}
	if res.HeapMB > 0 {
		resVals = append(resVals, "Maven heap "+heapText(res.HeapMB))
	}
	if res.TestForks != "" && res.TestForks != "none" {
		resVals = append(resVals, "-DforkCount="+res.TestForks)
	}
	if len(resVals) > 0 {
		proj.Rows = append(proj.Rows, layerRow{Name: "Resources", Values: resVals, Source: "Buildyard", Edit: "settings:defaults", Status: "used"})
	}

	for _, ly := range []*layer{&global, &repo, &proj} {
		for i := range ly.Rows {
			ly.Rows[i].Values = nonNil(ly.Rows[i].Values)
		}
		ly.Rows = nonNil(ly.Rows)
	}
	return projectLayers{
		Tool:   p.BuildTool,
		Layers: []layer{global, repo, proj},
		Order: "Maven reads the global files first, then the repository's .mvn files, then the command line. " +
			"Buildyard builds the command line from the project's settings and then the task's, so a task wins " +
			"over the project and the project wins over the global defaults.",
	}, nil
}

// summarize shows the few lines of a user config file that matter at a glance.
func summarize(cat toolconfig.Catalog, f toolconfig.File) []string {
	c, err := cat.Read(f.ID)
	if err != nil {
		return nil
	}
	var out []string
	switch f.Format {
	case toolconfig.FormatXML:
		for _, t := range []string{"localRepository", "offline", "activeProfile"} {
			for _, v := range xmlValues(c.Content, t) {
				out = append(out, t+": "+v)
			}
		}
		if n := strings.Count(c.Content, "<mirror>"); n > 0 {
			out = append(out, plural(n, "mirror"))
		}
		if n := strings.Count(c.Content, "<server>"); n > 0 {
			out = append(out, plural(n, "server"))
		}
		if n := strings.Count(c.Content, "<toolchain>"); n > 0 {
			out = append(out, plural(n, "toolchain"))
		}
	default:
		for _, l := range strings.Split(c.Content, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
	}
	return out
}

func xmlValues(s, name string) []string {
	var out []string
	open, end := "<"+name+">", "</"+name+">"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return out
		}
		s = s[i+len(open):]
		j := strings.Index(s, end)
		if j < 0 {
			return out
		}
		out = append(out, strings.TrimSpace(s[:j]))
		s = s[j:]
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func execName(mode string) string {
	switch mode {
	case "", maven.ModeAuto:
		return "auto (./mvnw, else mvnd, else mvn)"
	case maven.ModeWrapper:
		return "./mvnw"
	}
	return mode
}

func jdkText(j store.JDK) string {
	name := j.Name
	if name == "" {
		name = j.Vendor
	}
	t := j.Version
	if name != "" {
		t += " · " + name
	}
	return t
}

func heapText(mb int) string {
	if mb%1024 == 0 {
		return strconv.Itoa(mb/1024) + " GB"
	}
	return strconv.Itoa(mb) + " MB"
}

func pick(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func orText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nonEmpty(s string) []string {
	if s == "" {
		return []string{}
	}
	return []string{s}
}

func (s *Server) getenv(k string) string {
	if s.Getenv != nil {
		return s.Getenv(k)
	}
	return os.Getenv(k)
}
