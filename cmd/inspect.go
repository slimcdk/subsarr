package cmd

import (
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/slimcdk/subsarr/internal/archive"
	"github.com/slimcdk/subsarr/internal/catalogue"
	"github.com/slimcdk/subsarr/internal/ingest"
	"github.com/slimcdk/subsarr/internal/subfile"
	"github.com/spf13/cobra"
)

func inspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect-archive",
		Short: "Summarise a dump without importing it",
		Long: `Report what a dump contains: what its entries are, which file names
could not be parsed, whether it carries a catalogue and how that catalogue's
columns were read.

Nothing is extracted and nothing is written. Run this before an import to check
that your copy of the dump is read the way you expect.

Every entry's name is listed — that is free, it comes from the archive's header —
so the counts and the answer about the catalogue cover the whole dump. Opening an
entry is what costs: --sample bounds how many are opened to see what they hold.
Reading the catalogue costs about as long as parsing it, since a 7z archive is
read block by block and only the catalogue's own block has to be decompressed;
--skip-catalogue skips it when only the entry counts are wanted.

  subsarr inspect-archive --archive "/mnt/dump/Subscene V2.7z.001"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dumpFrom(cmd)
			if err != nil {
				// A catalogue on its own is worth reading: it is how an operator
				// checks the file they extracted before committing to an import.
				if d.catalogue == "" {
					return err
				}
				report := newArchiveReport(0)
				if err := report.readCatalogueFile(d.catalogue); err != nil {
					return err
				}
				report.printCatalogue(cmd.OutOrStdout())
				return nil
			}
			sample, _ := cmd.Flags().GetInt("sample")
			skipCatalogue, _ := cmd.Flags().GetBool("skip-catalogue")
			out := cmd.OutOrStdout()

			source, err := d.open()
			if err != nil {
				return err
			}
			defer func() { _ = source.Close() }()

			report := newArchiveReport(sample)
			if err := report.walk(source); err != nil {
				return err
			}

			// The entry summary first: it is the fast half, and on a dump whose
			// catalogue sits at the end it is all an operator gets for a while.
			report.printEntries(out)

			switch {
			case d.catalogue != "":
				if err := report.readCatalogueFile(d.catalogue); err != nil {
					return err
				}
			case report.catalogueEntry == nil:
				fmt.Fprintf(out, "\nCatalogue      none in this dump — an import would have to fall back to file names\n")
				return nil
			case skipCatalogue:
				fmt.Fprintf(out, "\nCatalogue      %s (entry %d of %d) — not read, --skip-catalogue was given\n",
					report.catalogueEntry.Name(), report.cataloguePosition, report.entries)
				return nil
			default:
				fmt.Fprintf(out, "\nReading the catalogue: %s, entry %d of %d …\n",
					report.catalogueEntry.Name(), report.cataloguePosition, report.entries)
				if err := report.readCatalogue(report.catalogueEntry); err != nil {
					return err
				}
			}

			report.printCatalogue(out)
			return nil
		},
	}

	cmd.Flags().String("archive", "", "Path to the first volume of the split 7z")
	cmd.Flags().String("files-db", "", "Path to an already-extracted dump directory")
	cmd.Flags().String("catalogue", "", "Read the catalogue from this file rather than from the dump")
	cmd.Flags().String("metadata", "", "V1 dump: path to metadata.json")
	cmd.Flags().String("subtitles", "", "V1 dump: path to the subtitles/ directory")
	cmd.Flags().Int("sample", 300, "Entries to open to see what they hold (0 = none); every entry's name is listed regardless")
	cmd.Flags().Bool("skip-catalogue", false, "Do not read the catalogue, only report whether the dump has one")

	return cmd
}

type archiveReport struct {
	sample int

	entries       int
	bytes         int64
	extensions    map[string]int
	unparseable   []string
	unparseableN  int
	sampledKinds  map[string]int
	sampled       int
	catalogue     *catalogue.Result
	catalogueName string

	// The catalogue is found by name while scanning and read afterwards, so that
	// the cheap half of the report can be printed first.
	catalogueEntry    archive.Entry
	cataloguePosition int

	languages  map[string]int
	withIMDB   int
	rowsWithID int
	sampleRows []catalogue.Upload
}

func newArchiveReport(sample int) *archiveReport {
	return &archiveReport{
		sample:       sample,
		extensions:   map[string]int{},
		sampledKinds: map[string]int{},
		languages:    map[string]int{},
	}
}

func (r *archiveReport) walk(source archive.Source) error {
	return source.Each(func(e archive.Entry) error {
		r.entries++
		r.bytes += e.Size()

		name := e.Name()
		ext := strings.ToLower(path.Ext(name))
		if ext == "" {
			ext = "(none)"
		}
		r.extensions[ext]++

		if ingest.IsCatalogue(name) {
			if r.catalogueEntry == nil {
				r.catalogueEntry = e
				r.cataloguePosition = r.entries
			}
			return nil
		}

		if catalogue.SubsceneIDFromPath(name) == "" {
			r.unparseableN++
			if len(r.unparseable) < 10 {
				r.unparseable = append(r.unparseable, name)
			}
		}

		// Only the first entries are opened: what a dump is made of shows up long
		// before the end of it, and opening every entry would be an import.
		if r.sampled < r.sample {
			r.sampled++
			r.sampleKind(e)
		}
		return nil
	})
}

// readCatalogueFile reads a catalogue an operator extracted from the dump, or
// one that ships beside it.
func (r *archiveReport) readCatalogueFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open catalogue: %w", err)
	}
	defer f.Close()
	return r.read(path, f, catalogueFormat(path))
}

func (r *archiveReport) readCatalogue(e archive.Entry) error {
	rc, err := e.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", e.Name(), err)
	}
	defer rc.Close()
	return r.read(e.Name(), rc, catalogueFormat(e.Name()))
}

func (r *archiveReport) read(name string, body io.Reader, format ingest.CatalogueFormat) error {
	read := catalogue.Read
	if format == ingest.CatalogueJSON {
		read = catalogue.ReadJSON
	}

	result, err := read(body, func(row catalogue.Upload) error {
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
		return fmt.Errorf("read %s: %w", name, err)
	}
	r.catalogue = &result
	r.catalogueName = name
	return nil
}

func (r *archiveReport) sampleKind(e archive.Entry) {
	rc, err := e.Open()
	if err != nil {
		r.sampledKinds["unreadable"]++
		return
	}
	defer rc.Close()

	// The same bound the importer reads an entry with, or this would report a
	// season pack as truncated and an import would store it.
	data, err := io.ReadAll(io.LimitReader(rc, subfile.MaxEntrySize+1))
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

func (r *archiveReport) printEntries(w io.Writer) {
	fmt.Fprintf(w, "Entries        %d (%s)\n", r.entries, humanBytes(r.bytes))

	fmt.Fprintf(w, "\nEntries by extension\n")
	for _, kv := range sortedCounts(r.extensions) {
		fmt.Fprintf(w, "  %-10s %8d\n", kv.key, kv.count)
	}

	if r.sampled > 0 {
		fmt.Fprintf(w, "\nContents of the first %d entries\n", r.sampled)
		for _, kv := range sortedCounts(r.sampledKinds) {
			fmt.Fprintf(w, "  %-16s %6d\n", kv.key, kv.count)
		}
	}

	if r.unparseableN > 0 {
		fmt.Fprintf(w, "\nFile names with no upload id: %d, for example\n", r.unparseableN)
		for _, name := range r.unparseable {
			fmt.Fprintf(w, "  %s\n", name)
		}
	}
}

func (r *archiveReport) printCatalogue(w io.Writer) {
	if r.catalogue == nil {
		return
	}

	fmt.Fprintf(w, "\nCatalogue      %s (table %q, %d rows, %d unreadable)\n",
		r.catalogueName, r.catalogue.Table, r.catalogue.Rows, r.catalogue.Skipped)
	fmt.Fprintf(w, "  columns      %s\n", strings.Join(r.catalogue.Columns, ", "))
	fmt.Fprintf(w, "  read as\n")
	for _, role := range catalogue.Roles() {
		column, ok := r.catalogue.Mapping[role]
		if !ok {
			column = "— not found —"
		}
		fmt.Fprintf(w, "    %-12s %s\n", role, column)
	}

	fmt.Fprintf(w, "  coverage     %s carry an IMDB id, %s carry an upload id\n",
		percent(r.withIMDB, r.catalogue.Rows), percent(r.rowsWithID, r.catalogue.Rows))
	if r.entries > 0 {
		// The two counts measure different things — rows in a file, entries in an
		// archive — so this is a ratio, not a coverage claim. Matching them one by
		// one would mean holding every id of both in memory.
		fmt.Fprintf(w, "  entries      %d rows for %d archive entries\n", r.catalogue.Rows, r.entries)
	}

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
