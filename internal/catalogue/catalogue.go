package catalogue

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/slimcdk/subsarr/internal/lang"
	titlepkg "github.com/slimcdk/subsarr/internal/title"
)

// Upload is one interpreted catalogue row.
type Upload struct {
	SubsceneID string
	FilePath   string
	Slug       string
	Title      string
	ImdbID     string
	Language   string
	HI         bool
	Year       int
	Author     string
	AuthorID   string
	Comment    string
	Releases   []string
	UploadedAt string
}

// Result describes what a read found. The mapping is reported, not assumed: the
// dump is a third-party artefact, and `inspect-archive` prints this so an
// operator can check how their copy was read before importing it.
type Result struct {
	Table   string
	Columns []string
	Mapping map[string]string // role → column name
	Rows    int
	Skipped int // rows whose tuple did not match the column list
}

// Roles a catalogue column can play.
const (
	RoleID       = "subscene_id"
	RolePath     = "file_path"
	RoleTitle    = "title"
	RoleIMDB     = "imdb_id"
	RoleLanguage = "language"
	RoleReleases = "releases"
	RoleAuthor   = "author"
	RoleAuthorID = "author_id"
	RoleComment  = "comment"
	RoleDate     = "uploaded_at"
	RoleSlug     = "slug"
)

// Roles are the catalogue's roles in reporting order.
func Roles() []string {
	return []string{
		RoleID, RolePath, RoleTitle, RoleIMDB, RoleLanguage, RoleReleases,
		RoleAuthor, RoleAuthorID, RoleComment, RoleDate, RoleSlug,
	}
}

// roleCandidates maps each role to the column names that mean it, most specific
// first, followed by the fragments a name may merely contain. The order of the
// roles themselves matters: `author_id` must be claimed before `id`.
var roleCandidates = []struct {
	role     string
	exact    []string
	contains []string
}{
	{RolePath, []string{"file_path", "filepath", "path", "file", "filename", "file_name", "zip", "zip_file"}, []string{"path", "file"}},
	{RoleIMDB, []string{"imdb_id", "imdb", "imdbid", "imdb_url", "imdb_link"}, []string{"imdb"}},
	{RoleAuthorID, []string{"author_id", "uploader_id", "user_id", "member_id"}, nil},
	{RoleAuthor, []string{"author", "uploader", "user", "member", "owner", "username"}, []string{"author", "uploader"}},
	{RoleLanguage, []string{"language", "lang", "language_name"}, []string{"lang"}},
	{RoleReleases, []string{"releases", "release", "release_name", "release_names", "rls"}, []string{"release"}},
	{RoleComment, []string{"comment", "comments", "note", "notes", "description"}, []string{"comment"}},
	{RoleDate, []string{"date", "upload_date", "uploaded_at", "created_at", "added", "datetime", "upload_time"}, []string{"date", "time"}},
	{RoleTitle, []string{"title", "movie_title", "movie_name", "movie", "name"}, []string{"title"}},
	{RoleSlug, []string{"slug", "movie_url", "url", "link", "original", "page"}, []string{"slug", "url"}},
	{RoleID, []string{"subscene_id", "sub_id", "subtitle_id", "id"}, []string{"subscene"}},
}

// Mapping works out which column plays which role.
func Mapping(columns []string) map[string]string {
	index := make(map[string]string, len(columns))
	for _, c := range columns {
		index[normaliseColumn(c)] = c
	}

	mapping := make(map[string]string, len(roleCandidates))
	used := make(map[string]struct{}, len(columns))

	claim := func(role, column string) {
		mapping[role] = column
		used[column] = struct{}{}
	}

	for _, candidate := range roleCandidates {
		for _, name := range candidate.exact {
			column, ok := index[name]
			if !ok {
				continue
			}
			if _, taken := used[column]; taken {
				continue
			}
			claim(candidate.role, column)
			break
		}
	}

	for _, candidate := range roleCandidates {
		if _, done := mapping[candidate.role]; done {
			continue
		}
		for _, fragment := range candidate.contains {
			for _, column := range columns {
				if _, taken := used[column]; taken {
					continue
				}
				if strings.Contains(normaliseColumn(column), fragment) {
					claim(candidate.role, column)
					break
				}
			}
			if _, done := mapping[candidate.role]; done {
				break
			}
		}
	}
	return mapping
}

func normaliseColumn(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.NewReplacer("-", "_", " ", "_").Replace(name)
}

// Read streams a catalogue dump, calling fn for every row.
//
// Rows are handed over as they are parsed: the dump is far too large to hold in
// memory, and the importer only ever needs one row at a time.
func Read(r io.Reader, fn func(Upload) error) (Result, error) {
	result := Result{Mapping: map[string]string{}}
	rd := &reader{br: bufio.NewReaderSize(r, 1<<20)}

	var positions map[string]int

	err := rd.scan(
		func(table string, columns []string) error {
			// A dump can declare several tables; the one with the columns that
			// look like a catalogue is the one we want.
			mapping := Mapping(columns)
			if _, ok := mapping[RoleID]; !ok {
				return nil
			}
			if _, ok := mapping[RolePath]; !ok {
				if _, ok := mapping[RoleTitle]; !ok {
					return nil
				}
			}
			result.Table = table
			result.Columns = columns
			result.Mapping = mapping
			positions = positionsOf(columns, mapping)
			return nil
		},
		func(values []string) error {
			if positions == nil {
				return nil
			}
			if len(values) < len(result.Columns) {
				result.Skipped++
				return nil
			}
			result.Rows++
			return fn(rowToUpload(values, positions))
		},
	)
	if err != nil {
		return result, err
	}
	if result.Table == "" {
		return result, fmt.Errorf("no catalogue table found in the dump")
	}
	return result, nil
}

func positionsOf(columns []string, mapping map[string]string) map[string]int {
	index := make(map[string]int, len(columns))
	for i, c := range columns {
		index[c] = i
	}
	positions := make(map[string]int, len(mapping))
	for role, column := range mapping {
		if i, ok := index[column]; ok {
			positions[role] = i
		}
	}
	return positions
}

func rowToUpload(values []string, positions map[string]int) Upload {
	at := func(role string) string {
		i, ok := positions[role]
		if !ok || i >= len(values) {
			return ""
		}
		return values[i]
	}

	upload := Upload{
		SubsceneID: at(RoleID),
		FilePath:   at(RolePath),
		Slug:       at(RoleSlug),
		Title:      at(RoleTitle),
		ImdbID:     NormaliseIMDB(at(RoleIMDB)),
		Language:   lang.Canonical(at(RoleLanguage)),
		Author:     html.UnescapeString(at(RoleAuthor)),
		AuthorID:   at(RoleAuthorID),
		Comment:    html.UnescapeString(at(RoleComment)),
		Releases:   ParseReleases(at(RoleReleases)),
		UploadedAt: ParseDate(at(RoleDate)),
	}
	return complete(upload)
}

// complete fills in what a catalogue does not state directly, whichever format it
// was written in: the slug and the id from the file's own path, the title from
// the slug when there is none, the year Subscene put in the slug, and the
// hearing-impaired flag, which lives in the file name and nowhere else.
func complete(u Upload) Upload {
	u.FilePath = strings.ReplaceAll(u.FilePath, `\`, "/")
	u.Slug = slugOf(u.Slug, u.FilePath)

	u.Title = html.UnescapeString(u.Title)
	if strings.TrimSpace(u.Title) == "" {
		u.Title = titlepkg.FromSlug(u.Slug)
	}
	if u.SubsceneID == "" {
		u.SubsceneID = SubsceneIDFromPath(u.FilePath)
	}
	u.HI = strings.Contains(strings.ToUpper(path.Base(u.FilePath)), "_HI_")
	u.Year = titlepkg.YearFromSlug(u.Slug)
	return u
}

var (
	imdbDigits  = regexp.MustCompile(`(\d{5,})`)
	subsceneID  = regexp.MustCompile(`-(\d+)(?:\.[A-Za-z0-9]+)?$`)
	slugFromURL = regexp.MustCompile(`/subtitles/([^/?#]+)`)
)

// NormaliseIMDB renders whatever the dump holds — an integer, a tt id, a URL — as
// the `tt` + at least seven digits form Bazarr sends.
func NormaliseIMDB(raw string) string {
	m := imdbDigits.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	digits := m[1]
	if len(digits) < 7 {
		digits = strings.Repeat("0", 7-len(digits)) + digits
	}
	return "tt" + digits
}

// slugOf takes the slug from the catalogue's own URL column when there is one,
// and otherwise from the directory the file sits in — which is how the archive is
// laid out.
func slugOf(raw, filePath string) string {
	if raw != "" {
		if m := slugFromURL.FindStringSubmatch(raw); m != nil {
			return m[1]
		}
		if !strings.Contains(raw, "/") && !strings.Contains(raw, " ") {
			return raw
		}
	}
	if dir := path.Dir(filePath); dir != "." && dir != "/" && dir != "" {
		return path.Base(dir)
	}
	return ""
}

// SubsceneIDFromPath recovers the upload id from an archive file name of the
// shape `<slug>_<language>-<id>.zip`.
//
// It is how an entry is matched to its catalogue row when the paths do not line
// up, how the ~350 entries the catalogue does not cover get an id at all, and how
// `inspect-archive` reports the names it could not read.
func SubsceneIDFromPath(filePath string) string {
	base := path.Base(filePath)
	base = strings.TrimSuffix(base, path.Ext(base))
	if m := subsceneID.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return ""
}

// ParseReleases reads the uploader's release list. The dump stores it as a JSON
// array where it has one, and as a separated list where it does not.
func ParseReleases(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	if strings.HasPrefix(raw, "[") {
		var releases []string
		if err := json.Unmarshal([]byte(raw), &releases); err == nil {
			return clean(releases)
		}
	}

	separator := "\n"
	switch {
	case strings.Contains(raw, "\n"):
	case strings.Contains(raw, "|"):
		separator = "|"
	case strings.Contains(raw, ";"):
		separator = ";"
	case strings.Contains(raw, ","):
		separator = ","
	}
	return clean(strings.Split(raw, separator))
}

func clean(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(html.UnescapeString(v))
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dateFormats are the shapes the dump's date column has been seen in, plus the
// ones a mysqldump of the same data would produce.
var dateFormats = []string{
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
	"1/2/2006 3:04 PM",
	"1/2/2006 15:04",
	"1/2/2006",
	"02/01/2006 15:04:05",
	"Jan 2, 2006",
	"2 Jan 2006",
}

// ParseDate renders an upload date as RFC 3339, or "" when it cannot be read.
// An unreadable date is better left empty than guessed: Bazarr shows it.
func ParseDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "0000-00-00") {
		return ""
	}
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil && unix > 1_000_000_000 && unix < 4_000_000_000 {
		return time.Unix(unix, 0).UTC().Format(time.RFC3339)
	}
	for _, layout := range dateFormats {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}
