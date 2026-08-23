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
  subsarr import-dump --metadata /mnt/dump/metadata.json --subtitles /mnt/dump/subtitles/

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

			d, err := dumpFrom(cmd)
			if err != nil {
				return err
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
			if !skipMetadata {
				if err := loadCatalogue(ctx, in, s.store, d, reload); err != nil {
					return err
				}
			}

			// After the catalogue and before a single entry is opened: a typo in
			// the whitelist has to stop the run now, not eight hours later with
			// nothing imported.
			if err := checkLanguages(ctx, s.store, languages); err != nil {
				return err
			}

			source, err := d.open()
			if err != nil {
				return err
			}
			defer func() { _ = source.Close() }()

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
	cmd.Flags().String("catalogue", "", "Read the catalogue from this file instead of from the archive")
	cmd.Flags().String("metadata", "", "V1 dump: path to metadata.json")
	cmd.Flags().String("subtitles", "", "V1 dump: path to the subtitles/ directory")
	cmd.Flags().String("languages", "", "Only store files for these languages (comma separated; empty = all)")
	cmd.Flags().Bool("dry-run", false, "Read and count without writing anything")
	cmd.Flags().Bool("resume", true, "Continue where a previous run stopped, skipping entries whose upload already has stored files; --resume=false re-reads everything, which is what a new dump needs")
	cmd.Flags().Bool("skip-metadata", false, "Skip the catalogue pass (for a dump that has none)")
	cmd.Flags().Bool("reload-metadata", false, "Reload the catalogue even if it is already loaded")
	cmd.Flags().Int("batch", 500, "Archive entries per transaction")
	cmd.Flags().Int("limit", 0, "Stop after this many entries (0 = all)")

	return cmd
}

// dumpFrom reads where the dump is from the flags. A V1 dump names its
// catalogue and its files separately; a V2 dump holds both.
func dumpFrom(cmd *cobra.Command) (dump, error) {
	flags := cmd.Flags()
	d := dump{}
	d.archive, _ = flags.GetString("archive")
	d.directory, _ = flags.GetString("files-db")
	d.catalogue, _ = flags.GetString("catalogue")

	metadata, _ := flags.GetString("metadata")
	subtitles, _ := flags.GetString("subtitles")
	if metadata != "" {
		d.catalogue = metadata
	}
	if subtitles != "" {
		d.directory = subtitles
	}

	if d.archive == "" && d.directory == "" {
		return d, fmt.Errorf("provide --archive (a split 7z), --files-db (an extracted directory), or --metadata with --subtitles (a V1 dump)")
	}
	return d, nil
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
// seen. It runs once the catalogue is loaded and before any entry is opened,
// which is the only moment where both facts are available and nothing has been
// wasted yet.
func checkLanguages(ctx context.Context, st store.Store, languages []string) error {
	if len(languages) == 0 {
		return nil
	}
	known, err := st.CatalogueLanguages(ctx)
	if err != nil {
		return err
	}
	if len(known) == 0 {
		// No catalogue at all — a dump imported with --skip-metadata. The names
		// were checked against the language table when they were parsed; there is
		// nothing else to check them against.
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
func loadCatalogue(ctx context.Context, in *ingest.Ingester, st store.Store, d dump, reload bool) error {
	loaded, err := st.CountUploads(ctx)
	if err != nil {
		return err
	}
	if loaded > 0 && !reload {
		log.Printf("[import] catalogue already loaded (%d uploads); pass 1 skipped — use --reload-metadata to force it", loaded)
		return nil
	}

	if d.catalogue != "" {
		f, err := os.Open(d.catalogue)
		if err != nil {
			return fmt.Errorf("open catalogue: %w", err)
		}
		defer f.Close()

		log.Printf("[import] pass 1/2: loading the catalogue from %s …", d.catalogue)
		result, err := in.LoadCatalogue(ctx, f, catalogueFormat(d.catalogue))
		if err != nil {
			return err
		}
		log.Printf("[import] catalogue: table %q, %d rows", result.Table, result.Rows)
		log.Printf("[import] columns read as: %s", describeMapping(result))
		return nil
	}

	source, err := d.open()
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	log.Print("[import] pass 1/2: loading the catalogue …")
	found := false
	scanned := 0
	err = source.Each(func(e archive.Entry) error {
		scanned++
		if !ingest.IsCatalogue(e.Name()) {
			return nil
		}

		// Entry names are free — they come from the archive's header — but
		// opening one is not: in a solid archive everything before it has to be
		// decompressed, and pass 2 then starts again from the front.
		if scanned > lateCatalogue {
			log.Printf("[import] the catalogue is entry %d of the archive; reading it decompresses "+
				"everything before it, and pass 2 will read the archive again from the start. "+
				"Extracting it once and passing --catalogue avoids that.", scanned)
		}
		rc, err := e.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", e.Name(), err)
		}
		defer rc.Close()

		result, err := in.LoadCatalogue(ctx, rc, catalogueFormat(e.Name()))
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

// lateCatalogue is how far into an archive the catalogue has to be before it is
// worth telling an operator that extracting it separately would be quicker.
const lateCatalogue = 100_000

func catalogueFormat(name string) ingest.CatalogueFormat {
	if strings.EqualFold(path.Ext(name), ".json") {
		return ingest.CatalogueJSON
	}
	return ingest.CatalogueSQL
}

func describeMapping(result catalogue.Result) string {
	roles := catalogue.Roles()
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

// dump is where a dump's parts are. A V2 dump is one archive; a V1 dump is a
// metadata.json plus a subtitles directory; either catalogue can also be handed
// over separately.
type dump struct {
	archive   string
	directory string
	catalogue string
}

func (d dump) open() (archive.Source, error) {
	if d.archive != "" {
		log.Printf("[import] opening archive %s …", d.archive)
		return archive.OpenSevenZip(d.archive)
	}
	return archive.OpenDir(d.directory)
}
