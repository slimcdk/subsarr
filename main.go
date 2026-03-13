package main

import (
	"log"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/slimcdk/subsarr/cmd/importcmd"
	"github.com/slimcdk/subsarr/internal/handlers"
	"github.com/slimcdk/subsarr/internal/sqlitedriver"
	_ "github.com/slimcdk/subsarr/pb_migrations"
)

func main() {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DBConnect: sqlitedriver.Connect,
	})

	// Run app migrations (pb_migrations/) on every bootstrap so the schema is
	// always up-to-date regardless of which subcommand is invoked.
	// migratecmd.Automigrate only watches for collection changes to generate
	// migration files — it does NOT apply them.
	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		return app.RunAppMigrations()
	})

	// Register the migrate CLI command and collection-change automigration.
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: true,
	})

	// Register custom REST routes.
	handlers.Register(app)

	// Register the CLI import command.
	importcmd.MustRegister(app)

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
