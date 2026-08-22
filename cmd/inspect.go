package cmd

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/slimcdk/subsarr/internal/archive"
	"github.com/slimcdk/subsarr/internal/catalogue"
	"github.com/slimcdk/subsarr/internal/subfile"
	"github.com/spf13/cobra"
)

// sampleSize is how many entries `inspect-archive` opens to see what they hold.
// Reading them all would be an import; reading a few hundred is enough to know
// what a dump is made of.
const sampleSize = 300

func inspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect-archive",
		Short: "Summarise a dump without importing it",
		Long: `Report what a dump contains: how its catalogue was read, how much of it
carries an IMDB id, which languages it holds, what its entries are, and which file
names could not be parsed.

Nothing is extracted and nothing is written. Run this before an import to check
that your copy of the dump is read the way you expect.

  subsarr inspect-archive --archive "/mnt/dump/Subscene V2.7z.001"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			archivePath, _ := cmd.Flags().GetString("archive")
			filesDB, _ := cmd.Flags().GetString("files-db")
			if archivePath == "" && filesDB == "" {
				return fmt.Errorf("provide --archive or --files-db")
			}
			limit, _ := cmd.Flags().GetInt("limit")
			out := cmd.OutOrStdout()

			source, err := openSource(archivePath, filesDB)
			if err != nil {
				return err
			}
			defer func() { _ = source.Close() }()

			report := newArchiveReport(limit)
			if err := report.walk(source); err != nil {
				return err
			}
			report.print(out)
			return nil
		},
	}

	cmd.Flags().String("archive", "", "Path to the first volume of the split 7z")
	cmd.Flags().String("files-db", "", "Path to an already-extracted dump directory")
	cmd.Flags().Int("limit", 0, "Stop after this many entries (0 = all)")

	return cmd
}

type archiveReport struct {
	limit int

	entries       int
	bytes         int64
	extensions    map[string]int
	unparseable   []string
	unparseableN  int
	sampledKinds  map[string]int
	sampled       int
	catalogue     *catalogue.Result
	catalogueName string

	languages  map[string]int
	withIMDB   int
	rowsWithID int
	sampleRows []catalogue.Upload
}

func newArchiveReport(limit int) *archiveReport {
	return &archiveReport{
		limit:        limit,
		extensions:   map[string]int{},
		sampledKinds: map[string]int{},
		languages:    map[string]int{},
	}
}

func (r *archiveReport) walk(source archive.Source) error {
	return source.Each(func(e archive.Entry) error {
		if r.limit > 0 && r.entries >= r.limit {
			return archive.ErrStop
		}
		r.entries++
		r.bytes += e.Size()

		name := e.Name()
		ext := strings.ToLower(path.Ext(name))
		if ext == "" {
			ext = "(none)"
		}
		r.extensions[ext]++

		if isCatalogue(name) {
			return r.readCatalogue(e)
		}

		if subsceneIDOf(name) == "" {
			r.unparseableN++
			if len(r.unparseable) < 10 {
				r.unparseable = append(r.unparseable, name)
			}
		}

		// Only the first entries are opened: what a dump is made of shows up long
		// before the end of it, and opening every entry would be an import.
		if r.sampled < sampleSize {
			r.sampled++
			r.sampleKind(e)
		}
		return nil
	})
}

func (r *archiveReport) readCatalogue(e archive.Entry) error {
	rc, err := e.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", e.Name(), err)
	}
	defer rc.Close()

	result, err := catalogue.Read(rc, func(row catalogue.Upload) error {
		if row.SubsceneID != "" {
			r.rowsWithID++
		}
		if row.ImdbID != "" {
			r.withIMDB++
		}
		r.languages[row.Language]++
		if len(r.sampleRows) < 3 {
			r.sampleRows = append(r.sampleRows, row)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("read %s: %w", e.Name(), err)
	}
	r.catalogue = &result
	r.catalogueName = e.Name()
	return nil
}

func (r *archiveReport) sampleKind(e archive.Entry) {
	rc, err := e.Open()
	if err != nil {
		r.sampledKinds["unreadable"]++
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, subfile.MaxFileSize+1))
	if err != nil {
		r.sampledKinds["unreadable"]++
		return
	}
	files, reason := subfile.Extract(e.Name(), data)
	if reason != subfile.ReasonNone {
		r.sampledKinds[string(reason)]++
		return
	}
	r.sampledKinds[fmt.Sprintf("%d subtitle(s)", len(files))]++
}

func (r *archiveReport) print(w io.Writer) {
	fmt.Fprintf(w, "Entries        %d (%s)\n", r.entries, humanBytes(r.bytes))

	fmt.Fprintf(w, "\nEntries by extension\n")
	for _, kv := range sortedCounts(r.extensions) {
		fmt.Fprintf(w, "  %-10s %8d\n", kv.key, kv.count)
	}

	fmt.Fprintf(w, "\nContents of the first %d entries\n", r.sampled)
	for _, kv := range sortedCounts(r.sampledKinds) {
		fmt.Fprintf(w, "  %-16s %6d\n", kv.key, kv.count)
	}

	if r.catalogue == nil {
		fmt.Fprintf(w, "\nCatalogue      none found — an import would have to fall back to file names\n")
	} else {
		fmt.Fprintf(w, "\nCatalogue      %s (table %q, %d rows, %d unreadable)\n",
			r.catalogueName, r.catalogue.Table, r.catalogue.Rows, r.catalogue.Skipped)
		fmt.Fprintf(w, "  columns      %s\n", strings.Join(r.catalogue.Columns, ", "))
		fmt.Fprintf(w, "  read as\n")
		for _, role := range []string{
			catalogue.RoleID, catalogue.RolePath, catalogue.RoleTitle, catalogue.RoleIMDB,
			catalogue.RoleLanguage, catalogue.RoleReleases, catalogue.RoleAuthor,
			catalogue.RoleAuthorID, catalogue.RoleComment, catalogue.RoleDate, catalogue.RoleSlug,
		} {
			column, ok := r.catalogue.Mapping[role]
			if !ok {
				column = "— not found —"
			}
			fmt.Fprintf(w, "    %-12s %s\n", role, column)
		}

		fmt.Fprintf(w, "  coverage     %s carry an IMDB id, %s carry an upload id\n",
			percent(r.withIMDB, r.catalogue.Rows), percent(r.rowsWithID, r.catalogue.Rows))
		fmt.Fprintf(w, "  entries      %s of the archive's entries have a catalogue row\n",
			percent(r.catalogue.Rows, r.entries))

		fmt.Fprintf(w, "\nLanguages (%d)\n", len(r.languages))
		for i, kv := range sortedCounts(r.languages) {
			if i >= 25 {
				fmt.Fprintf(w, "  … and %d more\n", len(r.languages)-25)
				break
			}
			name := kv.key
			if name == "" {
				name = "(unknown)"
			}
			fmt.Fprintf(w, "  %-24s %8d\n", name, kv.count)
		}

		fmt.Fprintf(w, "\nSample rows\n")
		for _, row := range r.sampleRows {
			fmt.Fprintf(w, "  %s  %q  %s  %s  %v\n  %s\n",
				row.SubsceneID, row.Title, row.ImdbID, row.Language, row.Releases, row.FilePath)
		}
	}

	if r.unparseableN > 0 {
		fmt.Fprintf(w, "\nFile names with no upload id: %d, for example\n", r.unparseableN)
		for _, name := range r.unparseable {
			fmt.Fprintf(w, "  %s\n", name)
		}
	}
}

// subsceneIDOf reports the upload id an entry's file name carries, if any.
func subsceneIDOf(name string) string {
	base := path.Base(name)
	stem := strings.TrimSuffix(base, path.Ext(base))
	dash := strings.LastIndex(stem, "-")
	if dash < 0 || dash == len(stem)-1 {
		return ""
	}
	for _, r := range stem[dash+1:] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return stem[dash+1:]
}

func percent(n, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%% (%d)", float64(n)*100/float64(total), n)
}

type counted struct {
	key   string
	count int
}

func sortedCounts(counts map[string]int) []counted {
	out := make([]counted, 0, len(counts))
	for key, count := range counts {
		out = append(out, counted{key, count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	return out
}
