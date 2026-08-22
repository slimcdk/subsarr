package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/slimcdk/subsarr/internal/archive"
	"github.com/slimcdk/subsarr/internal/catalogue"
	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/ingest"
	"github.com/slimcdk/subsarr/internal/lang"
	"github.com/slimcdk/subsarr/internal/store"
	"github.com/spf13/cobra"
)

func importCommand(cfg config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import-dump",
		Short: "Import a Subscene dump into the database",
		Long: `Import a Subscene dump.

The archive is read where it is: the split 7z is streamed, never extracted, so no
disk space is needed beyond the database and the subtitle files themselves.

  subsarr import-dump --archive "/mnt/dump/Subscene V2.7z.001"
  subsarr import-dump --files-db "/mnt/dump/Subscene Files DB/"

The import runs in two passes. The first loads the archive's catalogue — one row
per Subscene upload, with the title, IMDB id, release list, uploader, comment and
date that the file names never carried. The second walks every entry, extracts
the subtitle files it holds, and stores them under a content-addressed key.

Both passes are idempotent: re-running after an interruption, a new dump or a bug
fix updates what changed and duplicates nothing. Expect 8-12 hours for the full
archive on a NAS; the service keeps answering while it runs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()

			archivePath, _ := flags.GetString("archive")
			filesDB, _ := flags.GetString("files-db")
			if archivePath == "" && filesDB == "" {
				return fmt.Errorf("provide --archive (a split 7z) or --files-db (an extracted directory)")
			}

			languages, err := resolveLanguages(cmd, cfg)
			if err != nil {
				return err
			}

			opts := ingest.Options{Languages: languages}
			opts.DryRun, _ = flags.GetBool("dry-run")
			opts.Resume, _ = flags.GetBool("resume")
			opts.Batch, _ = flags.GetInt("batch")
			opts.Limit, _ = flags.GetInt("limit")

			s, err := open(ctx, cfg, true)
			if err != nil {
				return err
			}
			defer s.Close()

			if err := checkLanguages(ctx, s.store, languages); err != nil {
				return err
			}
			if len(languages) > 0 {
				log.Printf("[import] storing files for %d language(s): %s", len(languages), strings.Join(languages, ", "))
			}
			if opts.DryRun {
				log.Print("[import] dry run: nothing will be written")
			}

			started := time.Now()
			in := ingest.New(s.store, s.storage, opts)

			skipMetadata, _ := flags.GetBool("skip-metadata")
			reload, _ := flags.GetBool("reload-metadata")
			cataloguePath, _ := flags.GetString("catalogue")
			if !skipMetadata {
				if err := loadCatalogue(ctx, in, s.store, archivePath, filesDB, cataloguePath, reload); err != nil {
					return err
				}
			}

			source, err := openSource(archivePath, filesDB)
			if err != nil {
				return err
			}
			defer source.Close()

			if err := in.Run(ctx, source); err != nil {
				return err
			}

			stats := in.Stats()
			log.Printf("[import] done in %s: %s", time.Since(started).Round(time.Second), stats)

			if opts.DryRun {
				return nil
			}

			// The title index and the planner statistics are derived from the rows
			// that just changed. Searches stay correct without this, but fall back
			// to scanning.
			log.Print("[import] rebuilding the title index …")
			if err := s.store.Reindex(ctx); err != nil {
				return err
			}
			return s.store.Optimize(ctx)
		},
	}

	cmd.Flags().String("archive", "", "Path to the first volume of the split 7z — streamed, never extracted")
	cmd.Flags().String("files-db", "", "Path to an already-extracted dump directory")
	cmd.Flags().String("catalogue", "", "Read the catalogue from this SQL file instead of from the archive")
	cmd.Flags().String("languages", "", "Only store files for these languages (comma separated; empty = all)")
	cmd.Flags().Bool("dry-run", false, "Read and count without writing anything")
	cmd.Flags().Bool("resume", false, "Skip entries whose upload already has stored files")
	cmd.Flags().Bool("skip-metadata", false, "Skip the catalogue pass (for a dump that has none)")
	cmd.Flags().Bool("reload-metadata", false, "Reload the catalogue even if it is already loaded")
	cmd.Flags().Int("batch", 500, "Archive entries per transaction")
	cmd.Flags().Int("limit", 0, "Stop after this many entries (0 = all)")

	return cmd
}

// resolveLanguages reads the whitelist from the flag, falling back to the
// environment. The flag wins so that one run can differ from the deployment's
// standing configuration.
func resolveLanguages(cmd *cobra.Command, cfg config.Config) ([]string, error) {
	raw, _ := cmd.Flags().GetString("languages")
	if strings.TrimSpace(raw) == "" {
		raw = cfg.ImportLanguages
	}
	languages, err := lang.ParseList(raw)
	if err != nil {
		return nil, fmt.Errorf("--languages: %w", err)
	}
	return languages, nil
}

// checkLanguages refuses a whitelist naming a language the catalogue has never
// seen, before any pass runs. A typo would otherwise import nothing at all and
// only say so eight hours later.
func checkLanguages(ctx context.Context, st store.Store, languages []string) error {
	if len(languages) == 0 {
		return nil
	}
	known, err := st.CatalogueLanguages(ctx)
	if err != nil {
		return err
	}
	if len(known) == 0 {
		// Nothing is loaded yet, so there is nothing to check against.
		return nil
	}

	index := make(map[string]struct{}, len(known))
	for _, l := range known {
		index[l] = struct{}{}
	}
	var missing []string
	for _, l := range languages {
		if _, ok := index[l]; !ok {
			missing = append(missing, l)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no upload in the catalogue has language %s", strings.Join(missing, ", "))
	}
	return nil
}

// loadCatalogue runs the first pass. It is skipped when the catalogue is already
// loaded, because on the full archive it is an hour's work that changes nothing.
func loadCatalogue(ctx context.Context, in *ingest.Ingester, st store.Store, archivePath, filesDB, cataloguePath string, reload bool) error {
	loaded, err := st.CountUploads(ctx)
	if err != nil {
		return err
	}
	if loaded > 0 && !reload {
		log.Printf("[import] catalogue already loaded (%d uploads); pass 1 skipped — use --reload-metadata to force it", loaded)
		return nil
	}

	if cataloguePath != "" {
		f, err := os.Open(cataloguePath)
		if err != nil {
			return fmt.Errorf("open catalogue: %w", err)
		}
		defer f.Close()

		log.Printf("[import] pass 1/2: loading the catalogue from %s …", cataloguePath)
		result, err := in.LoadCatalogue(ctx, f)
		if err != nil {
			return err
		}
		log.Printf("[import] catalogue: table %q, %d rows", result.Table, result.Rows)
		log.Printf("[import] columns read as: %s", describeMapping(result))
		return nil
	}

	source, err := openSource(archivePath, filesDB)
	if err != nil {
		return err
	}
	defer source.Close()

	log.Print("[import] pass 1/2: loading the catalogue …")
	found := false
	err = source.Each(func(e archive.Entry) error {
		if !isCatalogue(e.Name()) {
			return nil
		}
		rc, err := e.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", e.Name(), err)
		}
		defer rc.Close()

		result, err := in.LoadCatalogue(ctx, rc)
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		found = true
		log.Printf("[import] catalogue %s: table %q, %d rows", e.Name(), result.Table, result.Rows)
		log.Printf("[import] columns read as: %s", describeMapping(result))
		return archive.ErrStop
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no catalogue (.sql) found in the dump — pass --skip-metadata to import file names only")
	}
	return nil
}

// isCatalogue recognises the archive's SQL dump. The name differs between
// mirrors of the dump, so the extension is what identifies it.
func isCatalogue(name string) bool {
	return strings.EqualFold(path.Ext(name), ".sql")
}

func describeMapping(result catalogue.Result) string {
	roles := []string{
		catalogue.RoleID, catalogue.RolePath, catalogue.RoleTitle, catalogue.RoleIMDB,
		catalogue.RoleLanguage, catalogue.RoleReleases, catalogue.RoleAuthor,
		catalogue.RoleAuthorID, catalogue.RoleComment, catalogue.RoleDate, catalogue.RoleSlug,
	}
	parts := make([]string, 0, len(roles))
	for _, role := range roles {
		column, ok := result.Mapping[role]
		if !ok {
			column = "—"
		}
		parts = append(parts, role+"="+column)
	}
	return strings.Join(parts, " ")
}

func openSource(archivePath, filesDB string) (archive.Source, error) {
	if archivePath != "" {
		log.Printf("[import] opening archive %s …", archivePath)
		return archive.OpenSevenZip(archivePath)
	}
	return archive.OpenDir(filesDB)
}
