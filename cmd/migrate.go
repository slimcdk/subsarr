package cmd

import (
	"fmt"

	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/database"
	"github.com/spf13/cobra"
)

func migrateCommand(cfg config.Config) *cobra.Command {
	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Bring the database schema up to date",
		Long: `Apply every pending migration.

An installation that predates versioned migrations is baselined automatically:
its existing schema is recorded as version 1 and the migrations after that one
carry it forward, including the data migration that moves the flat subtitles
table into the catalogue model.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := open(cmd.Context(), cfg, false)
			if err != nil {
				return err
			}
			defer s.Close()
			fmt.Println("schema is up to date")
			return nil
		},
	}

	migrate.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show which schema versions are applied",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := database.Open(cfg.DBDriver, cfg.DBDSN)
			if err != nil {
				return err
			}
			defer db.Close()

			migrator, err := database.NewMigrator(db, cfg.DBDriver)
			if err != nil {
				return err
			}
			lines, err := migrator.Status(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("%-5s %-24s %s\n", "VER", "MIGRATION", "APPLIED")
			for _, line := range lines {
				fmt.Println(line)
			}
			return nil
		},
	})

	return migrate
}
