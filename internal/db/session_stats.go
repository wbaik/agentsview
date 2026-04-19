// internal/db/session_stats.go
package db

import (
	"context"
	"time"
)

// StatsFilter mirrors the service-layer StatsFilter but lives in db
// because db functions take typed filters without cross-package deps.
type StatsFilter struct {
	Since           string
	Until           string
	Agent           string
	IncludeProjects []string
	ExcludeProjects []string
	Timezone        string
	GHToken         string
}

// GetSessionStats computes the v1 session-stats JSON response.
// Stub returns a mostly-empty response so the command compiles and
// callers can incrementally fill sections in subsequent tasks.
func (db *DB) GetSessionStats(ctx context.Context, f StatsFilter) (*SessionStats, error) {
	return &SessionStats{
		SchemaVersion: 1,
		Window:        StatsWindow{},
		Filters: StatsFilters{
			Agent:            orDefault(f.Agent, "all"),
			ProjectsIncluded: f.IncludeProjects,
			ProjectsExcluded: nonNilSlice(f.ExcludeProjects),
			Timezone:         f.Timezone,
		},
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
