package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/binarycat0/buildyard/internal/jdk"
)

// JDK is a JDK known to Buildyard.
type JDK struct {
	ID      int64  `json:"id"`
	Home    string `json:"home"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Major   int    `json:"major"`
	Vendor  string `json:"vendor"`
	Arch    string `json:"arch"`
	Source  string `json:"source"`
	Missing bool   `json:"missing"`
}

const jdkCols = `id, home, name, version, major, vendor, arch, source, missing`

func scanJDK(r scanner) (JDK, error) {
	var j JDK
	err := r.Scan(&j.ID, &j.Home, &j.Name, &j.Version, &j.Major, &j.Vendor, &j.Arch, &j.Source, &j.Missing)
	return j, err
}

// ListJDKs returns all JDKs, newest major first.
func (s *Store) ListJDKs(ctx context.Context) ([]JDK, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jdkCols+` FROM jdk ORDER BY missing, major DESC, version DESC, home`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JDK{}
	for rows.Next() {
		j, err := scanJDK(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetJDK returns one JDK.
func (s *Store) GetJDK(ctx context.Context, id int64) (JDK, error) {
	j, err := scanJDK(s.db.QueryRowContext(ctx, `SELECT `+jdkCols+` FROM jdk WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return JDK{}, ErrNotFound
	}
	return j, err
}

// AddJDK inserts a manually added JDK.
func (s *Store) AddJDK(ctx context.Context, info jdk.Info, name string) (JDK, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO jdk (home, name, version, major, vendor, arch, source, detected_at)
		VALUES (?, ?, ?, ?, ?, ?, 'manual', ?)`,
		info.Home, name, info.Version, info.Major, info.Vendor, info.Arch, now())
	if err != nil {
		return JDK{}, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetJDK(ctx, id)
}

// RenameJDK sets the user label.
func (s *Store) RenameJDK(ctx context.Context, id int64, name string) (JDK, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE jdk SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return JDK{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return JDK{}, ErrNotFound
	}
	return s.GetJDK(ctx, id)
}

// DeleteJDK removes a JDK; projects using it fall back to the default.
func (s *Store) DeleteJDK(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jdk WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncResult summarises a detection run.
type SyncResult struct {
	Found   int `json:"found"`
	Added   int `json:"added"`
	Missing int `json:"missing"`
}

// SyncDetectedJDKs upserts what detection found. Entries not found this time are marked missing
// rather than deleted, so projects keep their selection; a manual entry stays available while
// exists(home) is true. User labels are kept.
func (s *Store) SyncDetectedJDKs(ctx context.Context, found []jdk.Info, exists func(home string) bool) (SyncResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncResult{}, err
	}
	defer tx.Rollback()

	known := map[string]string{} // home → source
	rows, err := tx.QueryContext(ctx, `SELECT home, source FROM jdk`)
	if err != nil {
		return SyncResult{}, err
	}
	for rows.Next() {
		var home, source string
		if err := rows.Scan(&home, &source); err != nil {
			rows.Close()
			return SyncResult{}, err
		}
		known[home] = source
	}
	rows.Close()

	res := SyncResult{Found: len(found)}
	ts := now()
	seen := map[string]bool{}
	for _, f := range found {
		seen[f.Home] = true
		if _, ok := known[f.Home]; !ok {
			res.Added++
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jdk (home, version, major, vendor, arch, source, detected_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(home) DO UPDATE SET version = excluded.version, major = excluded.major,
				vendor = excluded.vendor, arch = excluded.arch, missing = 0, detected_at = excluded.detected_at`,
			f.Home, f.Version, f.Major, f.Vendor, f.Arch, f.Source, ts); err != nil {
			return res, err
		}
	}
	for home, source := range known {
		if seen[home] {
			continue
		}
		missing := source != "manual" || !exists(home)
		if missing {
			res.Missing++
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jdk SET missing = ? WHERE home = ?`, missing, home); err != nil {
			return res, err
		}
	}
	return res, tx.Commit()
}
