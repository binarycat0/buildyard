package httpapi

import (
	"context"
	"strings"

	"github.com/binarycat0/buildyard/internal/store"
)

// failedModules returns the -pl references of a job's failed modules plus every module
// in the reactor that depends on them (they would fail too, or build against stale
// artifacts). Modules are matched to the last discovery by key, or by name for runs
// whose progress came from the log; unknown keys are used as they are.
func (s *Server) failedModules(ctx context.Context, j store.Job) ([]string, error) {
	jms, err := s.Store.ListJobModules(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	mods, err := s.Store.ListModules(ctx, j.ProjectID)
	if err != nil {
		return nil, err
	}
	byKey := map[string]store.Module{}
	byName := map[string]string{}
	perArtifact := map[string]int{}
	users := map[string][]string{}
	for _, m := range mods {
		byKey[m.Key] = m
		byName[m.Name] = m.Key
		perArtifact[m.ArtifactID]++
		for _, d := range m.Deps {
			users[d] = append(users[d], m.Key)
		}
	}
	var refs []string
	seen := map[string]bool{}
	var queue []string
	for _, jm := range jms {
		if jm.Status != "failed" {
			continue
		}
		key := jm.Key
		if _, ok := byKey[key]; !ok {
			if k, ok := byName[jm.Name]; ok {
				key = k
			} else if !strings.Contains(key, ":") || strings.ContainsAny(key, " ") {
				continue // a display name we cannot map to a module
			}
		}
		if !seen[key] {
			seen[key] = true
			queue = append(queue, key)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, u := range users[queue[i]] {
			if !seen[u] {
				seen[u] = true
				queue = append(queue, u)
			}
		}
	}
	for _, key := range queue {
		m, ok := byKey[key]
		switch {
		case ok && perArtifact[m.ArtifactID] == 1:
			refs = append(refs, ":"+m.ArtifactID)
		default:
			refs = append(refs, key)
		}
	}
	return refs, nil
}
