package main

import "context"

// archiveSessionRepository supplies a fresh snapshot, including unarchived sessions
// needed to protect shared checkouts. Unavailable or incomplete data is an error,
// never an empty successful snapshot. Implementations do not modify the sessions.
type archiveSessionRepository interface {
	List(context.Context) ([]archivedSession, error)
}
