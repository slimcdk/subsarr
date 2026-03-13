package main

import (
	"log"

	"github.com/pocketbase/pocketbase"
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

	// Always automigrate — migrations are idempotent and this ensures the schema
	// is ready whether the binary is invoked as "serve" or "import-dump".
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
