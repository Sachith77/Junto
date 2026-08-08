package domain

// Pagination is keyset (cursor) based, never OFFSET.
//
// This is not a performance preference. Under concurrent inserts — which is the entire
// premise of this application — OFFSET pagination silently duplicates and skips rows: an
// insert before the current offset shifts every subsequent page by one. A cursor anchored
// to an actual row is stable regardless of what else is being written.
//
// The cursor is opaque to clients on purpose. It encodes the sort key of the last row seen,
// and making it opaque means the sort key can change without breaking anyone's saved
// pagination state.

// DefaultPageSize is used when a request omits a limit.
const DefaultPageSize = 25

// MaxPageSize caps how much one request can ask for, so a client cannot turn a list
// endpoint into an unbounded table scan.
const MaxPageSize = 100

// Cursor is an opaque pagination position.
type Cursor string

// PageRequest asks for one page of results.
type PageRequest struct {
	Limit int
	After Cursor
}

// Normalize clamps the limit into range and applies the default. Called by services, so
// no repository ever sees an out-of-range limit regardless of what a handler passes.
func (p PageRequest) Normalize() PageRequest {
	switch {
	case p.Limit <= 0:
		p.Limit = DefaultPageSize
	case p.Limit > MaxPageSize:
		p.Limit = MaxPageSize
	}
	return p
}

// Page is one page of results plus the cursor needed to fetch the next.
type Page[T any] struct {
	Items      []T
	NextCursor Cursor
	HasMore    bool
}

// NewPage builds a page from a slice fetched with limit+1 rows.
//
// Fetching one extra row is how HasMore is determined without a second COUNT query — and
// a COUNT would be both slower and wrong, since the total can change between the two
// queries. The extra row is trimmed before returning.
func NewPage[T any](rows []T, limit int, cursorOf func(T) Cursor) Page[T] {
	page := Page[T]{}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	page.Items = rows
	if len(rows) > 0 && page.HasMore {
		page.NextCursor = cursorOf(rows[len(rows)-1])
	}
	return page
}
