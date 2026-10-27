package memory

import "context"

// MaxList is how many memories a listing returns at most.
const MaxList = 200

// Listing is what a dialog lists of the memories the agent has collected:
// the memories themselves, and how many there are beyond them.
type Listing struct {
	// Memories are the core, global and project ones, the latest updated
	// first.
	Memories []Entry
	// Total is how many memories the scope holds, those hidden included.
	Total int
	// Hidden is how many archived or expired memories are left out, 0 when
	// they were asked for.
	Hidden int
}

// List returns the memories a dialog of the owner lists: the global ones,
// core ones among them, and those of project, the latest updated first.
// Archived and expired memories are left out unless all is true, and
// Listing.Hidden says how many of them there are. At most limit memories are
// returned, MaxList if limit is not positive; Listing.Total says how many
// there are.
func (s *Store) List(ctx context.Context, project string, all bool, limit int) (Listing, error) {
	if limit <= 0 || limit > MaxList {
		limit = MaxList
	}
	// A memory without a project is global; those of a project are those of
	// the one shown, never of another.
	where, args := "project=''", []any{}
	if project != "" {
		where, args = "(project='' OR project=?)", []any{project}
	}
	var out Listing
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM memories WHERE "+where, args...).Scan(&out.Total); err != nil {
		return Listing{}, err
	}
	if !all {
		hidden := "archived=1 OR (expires<>'' AND expires<=?)"
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM memories WHERE "+where+" AND ("+hidden+")",
			append(append([]any{}, args...), now())...).Scan(&out.Hidden); err != nil {
			return Listing{}, err
		}
		where += " AND " + current
		args = append(args, now())
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM memories WHERE "+where+" ORDER BY updated DESC,id LIMIT ?",
		append(args, limit)...)
	if err != nil {
		return Listing{}, err
	}
	out.Memories, err = entries(rows)
	return out, err
}
