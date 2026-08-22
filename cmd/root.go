// Package cmd is subsarr's command line.
//
// Four commands cover the whole life of an installation: `migrate` brings the
// schema up to date, `import-dump` loads an archive, `serve` answers Bazarr, and
// `prune` reclaims space. `inspect-archive` looks at a dump without importing it.
package cmd

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/database"
	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
	"github.com/slimcdk/subsarr/internal/version"
	"github.com/spf13/cobra"
)

// Root builds the command tree.
func Root(cfg config.Config) *cobra.Command {
	root := &cobra.Command{
		Use:     "subsarr",
		Short:   "Self-hosted Subscene subtitle provider",
		Version: version.String(),
		// A failing command has already said what went wrong; cobra printing the
		// usage screen on top of it only buries the message.
		SilenceUsage: true,
	}
	root.AddCommand(
		serveCommand(cfg),
		migrateCommand(cfg),
		importCommand(cfg),
		inspectCommand(),
		pruneCommand(cfg),
	)
	return root
}

// session is an opened database with its schema up to date, plus storage.
type session struct {
	db      *sql.DB
	store   store.Store
	storage storage.Store
}

func (s *session) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// open connects, migrates and wires everything a command needs. Migrating on
// every command is deliberate: an operator should never have to remember to run
// it, and it is a no-op once the schema is current.
func open(ctx context.Context, cfg config.Config, withStorage bool) (*session, error) {
	db, err := database.Open(cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	migrator, err := database.NewMigrator(db, cfg.DBDriver)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := migrator.Up(ctx); err != nil {
		db.Close()
		return nil, err
	}

	st, err := store.New(db, cfg.DBDriver)
	if err != nil {
		db.Close()
		return nil, err
	}

	s := &session{db: db, store: st}
	if withStorage {
		if s.storage, err = storage.New(cfg); err != nil {
			db.Close()
			return nil, fmt.Errorf("storage: %w", err)
		}
	}
	return s, nil
}
