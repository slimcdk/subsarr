package cmd

import (
	"log"
	"net/http"

	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/server"
	"github.com/slimcdk/subsarr/internal/version"
	"github.com/spf13/cobra"
)

func serveCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			s, err := open(ctx, cfg, true)
			if err != nil {
				return err
			}
			defer s.Close()

			// Brings the title index and planner statistics up to date. It does
			// nothing unless the data changed, so restarts stay fast.
			if err := s.store.Optimize(ctx); err != nil {
				return err
			}

			srv := server.New(s.store, s.storage, version.String())
			// The language list aggregates the whole database. Loading it now
			// means Bazarr's first request does not have to.
			if err := srv.Warm(ctx); err != nil {
				return err
			}

			log.Printf("subsarr %s listening on %s (driver=%s)", version.String(), cfg.Listen, cfg.DBDriver)
			return http.ListenAndServe(cfg.Listen, srv.Routes())
		},
	}
}
