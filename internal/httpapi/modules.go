package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/binarycat0/buildyard/internal/buildtool/maven"
	"github.com/binarycat0/buildyard/internal/store"
)

// TaskModulesDiscover is the background task kind for module discovery.
const TaskModulesDiscover = "modules.discover"

type modulesResponse struct {
	Modules      []store.Module     `json:"modules"`
	DiscoveredAt *time.Time         `json:"discoveredAt"`
	SkipPresets  []maven.SkipPreset `json:"skipPresets"`    // defaults merged with the project's own
	Defaults     []maven.SkipPreset `json:"defaultPresets"` // built-in presets, for the editor
}

func (s *Server) listModules(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	mods, err := s.Store.ListModules(r.Context(), p.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	resp := modulesResponse{Modules: mods, SkipPresets: presetsFor(p), Defaults: maven.DefaultSkipPresets()}
	if len(mods) > 0 {
		resp.DiscoveredAt = &mods[0].DiscoveredAt
	}
	writeJSON(w, http.StatusOK, resp)
}

// discoveryResult is stored on the finished task.
type discoveryResult struct {
	Modules int      `json:"modules"`
	Argv    []string `json:"argv"`
	Offline bool     `json:"offline"`
	Seconds float64  `json:"seconds"`
}

// refreshModules starts discovery as a background task. The body may carry the run
// form's spec: its profiles, skips and properties can change the reactor.
func (s *Server) refreshModules(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	var req runRequest
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
			return
		}
	}
	var spec maven.RunSpec
	if req.Spec != nil {
		spec = *req.Spec
	}
	t, err := s.SubmitModuleDiscovery(p, spec)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

// SubmitModuleDiscovery enqueues discovery for a project (one at a time per project).
func (s *Server) SubmitModuleDiscovery(p store.Project, spec maven.RunSpec) (any, error) {
	key := TaskModulesDiscover + ":" + strconv.FormatInt(p.ID, 10)
	return s.Tasks.Submit(TaskModulesDiscover, key, func(ctx context.Context) (any, error) {
		return s.discover(ctx, p, spec.Normalize())
	})
}

func (s *Server) discover(ctx context.Context, p store.Project, spec maven.RunSpec) (discoveryResult, error) {
	pv, cmd, err := s.prepare(ctx, p, maven.RunSpec{Goals: []string{"validate"}})
	if err != nil {
		return discoveryResult{}, err
	}
	if pv.Error != "" {
		return discoveryResult{}, errors.New(pv.Error)
	}
	exe := cmd.Argv[0]
	presets := presetsFor(p)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	start := time.Now()
	var lastOut []byte
	var lastErr error
	// Offline first: fast and no network. If the help plugin is not in the local
	// repository yet, retry once online.
	for _, offline := range []bool{true, false} {
		argv := append([]string{exe}, maven.DiscoverArgs(spec, presets, offline)...)
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir, c.Env = cmd.Dir, cmd.Env
		out, err := c.CombinedOutput()
		mods := maven.ParseDiscovery(out)
		if err == nil && len(mods) > 0 {
			stored := make([]store.Module, len(mods))
			for i, m := range mods {
				stored[i] = store.Module{GroupID: m.GroupID, ArtifactID: m.ArtifactID, Name: m.Name,
					Packaging: m.Packaging, Profiles: m.Profiles}
			}
			if err := s.Store.ReplaceModules(ctx, p.ID, stored, "maven"); err != nil {
				return discoveryResult{}, err
			}
			return discoveryResult{Modules: len(mods), Argv: argv, Offline: offline,
				Seconds: time.Since(start).Round(100 * time.Millisecond).Seconds()}, nil
		}
		lastOut, lastErr = out, err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no modules in the output")
	}
	return discoveryResult{}, fmt.Errorf("%v\n%s", lastErr, tail(lastOut, 25))
}

// tail returns the last n lines of the output, without Maven's download noise.
func tail(out []byte, n int) string {
	var keep []string
	for _, l := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		if strings.Contains(l, "Download") || strings.HasPrefix(l, "Picked up JAVA_TOOL_OPTIONS") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

// ---------- saved tasks (run profiles) ----------

type profileRequest struct {
	Name   string          `json:"name"`
	Spec   json.RawMessage `json:"spec"`
	Pinned bool            `json:"pinned"`
}

func (s *Server) checkProfile(p store.Project, req *profileRequest) (string, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return "", errors.New("name must not be empty")
	}
	var spec maven.RunSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		return "", errors.New("spec: " + err.Error())
	}
	spec = spec.Normalize()
	if err := spec.Validate(presetsFor(p)); err != nil {
		return "", err
	}
	b, _ := json.Marshal(spec)
	return string(b), nil
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	list, err := s.Store.ListProfiles(r.Context(), p.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	var req profileRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	spec, err := s.checkProfile(p, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	prof, err := s.Store.CreateProfile(r.Context(), store.RunProfile{ProjectID: p.ID, Name: req.Name, Spec: json.RawMessage(spec), Pinned: req.Pinned})
	s.respondProfile(w, prof, err, http.StatusCreated)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	prof, err := s.Store.GetProfile(r.Context(), id)
	if err != nil {
		s.respondProfile(w, prof, err, 0)
		return
	}
	p, err := s.Store.GetProject(r.Context(), prof.ProjectID)
	if err != nil {
		s.respondProject(w, p, err)
		return
	}
	var req profileRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	spec, err := s.checkProfile(p, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	prof, err = s.Store.UpdateProfile(r.Context(), store.RunProfile{ID: id, Name: req.Name, Spec: json.RawMessage(spec), Pinned: req.Pinned})
	s.respondProfile(w, prof, err, http.StatusOK)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteProfile(r.Context(), id); err != nil {
		s.respondProfile(w, store.RunProfile{}, err, 0)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) respondProfile(w http.ResponseWriter, p store.RunProfile, err error, status int) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "task not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "a task with this name already exists")
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, status, p)
	}
}
