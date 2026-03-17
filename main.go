package main

import (
	"log"
	"net/http"
	"os"

	"github.com/slimcdk/subsarr/cmd/importcmd"
	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/database"
	"github.com/slimcdk/subsarr/internal/server"
	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
	"github.com/spf13/cobra"
)

func main() {
	cfg := config.Load()

	root := &cobra.Command{
		Use:   "subsarr",
		Short: "Self-hosted Subscene subtitle provider",
	}

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE: func(_ *cobra.Command, _ []string) error {
			db, err := database.Open(cfg.DBDriver, cfg.DBDSN)
			if err != nil {
				return err
			}
			defer db.Close()

			if err := database.Migrate(db, cfg.DBDriver); err != nil {
				return err
			}

			st, err := store.New(db, cfg.DBDriver)
			if err != nil {
				return err
			}

			stor, err := storage.New(cfg)
			if err != nil {
				return err
			}

			srv := server.New(st, stor)
			log.Printf("listening on %s (driver=%s)", cfg.Listen, cfg.DBDriver)
			return http.ListenAndServe(cfg.Listen, srv.Routes())
		},
	}

	root.AddCommand(serve, importcmd.NewCommand(cfg))

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
