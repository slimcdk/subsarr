package cmd

import (
	"fmt"
	"log"
	"strings"

	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/lang"
	"github.com/spf13/cobra"
)

func pruneCommand(cfg config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove stored files for languages you do not keep",
		Long: `Delete the subtitle files of languages outside the given list.

Pruning is always explicit. Neither an import nor a server start will ever run
it: it is the one command that removes data, and it must be asked for.

The catalogue is kept in full, so a later import with a wider whitelist restores
the files without re-reading the metadata. Storage objects are shared between
identical subtitles, so an object is only removed once no remaining file points
at it.

  subsarr prune --keep-languages english,danish --dry-run
  subsarr prune --keep-languages english,danish`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			raw, _ := cmd.Flags().GetString("keep-languages")
			keep, err := lang.ParseList(raw)
			if err != nil {
				return fmt.Errorf("--keep-languages: %w", err)
			}
			if len(keep) == 0 {
				return fmt.Errorf("--keep-languages is required: pruning without a list would delete every subtitle")
			}

			dryRun, _ := cmd.Flags().GetBool("dry-run")
			batch, _ := cmd.Flags().GetInt("batch")

			s, err := open(ctx, cfg, true)
			if err != nil {
				return err
			}
			defer s.Close()

			rows, bytes, err := s.store.PruneScope(ctx, keep)
			if err != nil {
				return err
			}
			log.Printf("[prune] keeping %s", strings.Join(keep, ", "))
			if rows > 0 && bytes == 0 {
				// Rows migrated from the flat model carry no size: it was never
				// recorded. They still occupy storage.
				log.Printf("[prune] %d subtitle files are outside the list; their size is unknown "+
					"until an import has seen them", rows)
			} else {
				log.Printf("[prune] %d subtitle files (%s) are outside the list", rows, humanBytes(bytes))
			}

			if dryRun {
				log.Print("[prune] dry run: nothing was deleted")
				return nil
			}
			if rows == 0 {
				return nil
			}

			var deleted, objects int
			for {
				result, err := s.store.PruneLanguages(ctx, keep, batch)
				if err != nil {
					return err
				}
				if result.Deleted == 0 {
					break
				}
				deleted += result.Deleted

				for _, key := range result.OrphanKeys {
					if err := s.storage.Delete(ctx, key); err != nil {
						log.Printf("[prune] could not delete %s: %v", key, err)
						continue
					}
					objects++
				}
				log.Printf("[prune] %d files removed, %d storage objects deleted", deleted, objects)
			}

			log.Print("[prune] rebuilding the title index …")
			if err := s.store.Reindex(ctx); err != nil {
				return err
			}
			return s.store.Optimize(ctx)
		},
	}

	cmd.Flags().String("keep-languages", "", "Languages to keep (comma separated) — everything else is deleted")
	cmd.Flags().Bool("dry-run", false, "Report what would be deleted without deleting it")
	cmd.Flags().Int("batch", 1000, "Rows deleted per transaction")

	return cmd
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
