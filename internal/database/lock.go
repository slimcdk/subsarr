package database

import (
	"time"

	"github.com/pressly/goose/v3/lock"
)

// newPostgresLocker returns the advisory lock goose takes for the duration of a
// migration run. It holds the lock on its own connection, so a second container
// starting at the same time waits instead of running the same data migration
// concurrently.
func newPostgresLocker() (lock.SessionLocker, error) {
	return lock.NewPostgresSessionLocker(
		lock.WithLockTimeout(10, 60),   // probe every 10s for up to 10 minutes
		lock.WithUnlockTimeout(10, 30), // give up releasing after 5 minutes
	)
}

// migrationTimeout bounds a single migration command run from the CLI.
const migrationTimeout = 24 * time.Hour
