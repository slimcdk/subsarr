package catalogue

import (
	"strings"
	"testing"
)

// A mysqldump of the shape the archive ships: a CREATE TABLE followed by extended
// INSERTs, with everything a user could type in the free-text columns.
const dump = "" +
	"-- MySQL dump 10.13\n" +
	"/*!40101 SET NAMES utf8mb4 */;\n" +
	"DROP TABLE IF EXISTS `all_subs`;\n" +
	"CREATE TABLE `all_subs` (\n" +
	"  `id` int(11) NOT NULL,\n" +
	"  `movie_url` varchar(255) DEFAULT NULL,\n" +
	"  `title` varchar(255) DEFAULT NULL,\n" +
	"  `imdb` varchar(20) DEFAULT NULL,\n" +
	"  `language` varchar(64) DEFAULT NULL,\n" +
	"  `releases` text,\n" +
	"  `author` varchar(128) DEFAULT NULL,\n" +
	"  `author_id` int(11) DEFAULT NULL,\n" +
	"  `comment` text,\n" +
	"  `date` datetime DEFAULT NULL,\n" +
	"  `file_path` varchar(512) DEFAULT NULL,\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  KEY `idx_lang` (`language`)\n" +
	") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n" +
	"INSERT INTO `all_subs` VALUES " +
	"(1120553,'https://subscene.com/subtitles/the-dark-knight','The Dark Knight','tt0468569','English'," +
	"'TDK.720p.BluRay\\nTDK.1080p','someone',42,'Synced (and fixed) by \\'me\\'','2008-07-20 13:45:00'," +
	"'Subscene Files DB/the-dark-knight/the-dark-knight_english-1120553.zip')," +
	"(1120554,NULL,'It''s a Wonderful Life; or, Life',468569,'Brazillian Portuguese',NULL,NULL,NULL,''," +
	"'0000-00-00 00:00:00','Subscene Files DB/its-a-wonderful-life-1946/its-a-wonderful-life-1946_HI_brazillian-portuguese-1120554.zip');\n"

func readAll(t *testing.T, source string) ([]Upload, Result) {
	t.Helper()
	var rows []Upload
	result, err := Read(strings.NewReader(source), func(u Upload) error {
		rows = append(rows, u)
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rows, result
}

func TestRead(t *testing.T) {
	rows, result := readAll(t, dump)

	if result.Table != "all_subs" {
		t.Errorf("table = %q, want all_subs", result.Table)
	}
	if len(result.Columns) != 11 {
		t.Errorf("columns = %v, want 11 (the keys are not columns)", result.Columns)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	first := rows[0]
	if first.SubsceneID != "1120553" {
		t.Errorf("subscene id = %q", first.SubsceneID)
	}
	if first.Title != "The Dark Knight" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Slug != "the-dark-knight" {
		t.Errorf("slug = %q", first.Slug)
	}
	if first.ImdbID != "tt0468569" {
		t.Errorf("imdb = %q", first.ImdbID)
	}
	if first.Language != "english" {
		t.Errorf("language = %q, want the canonical spelling", first.Language)
	}
	if len(first.Releases) != 2 || first.Releases[0] != "TDK.720p.BluRay" {
		t.Errorf("releases = %v", first.Releases)
	}
	if first.Author != "someone" || first.AuthorID != "42" {
		t.Errorf("author = %q/%q", first.Author, first.AuthorID)
	}
	if first.Comment != "Synced (and fixed) by 'me'" {
		t.Errorf("comment = %q — an escaped quote and a bracket must survive", first.Comment)
	}
	if first.UploadedAt != "2008-07-20T13:45:00Z" {
		t.Errorf("uploaded_at = %q", first.UploadedAt)
	}
	if first.FilePath != "Subscene Files DB/the-dark-knight/the-dark-knight_english-1120553.zip" {
		t.Errorf("file path = %q", first.FilePath)
	}
	if first.HI {
		t.Error("this upload is not hearing impaired")
	}
}

func TestRead_SecondRow(t *testing.T) {
	rows, _ := readAll(t, dump)
	second := rows[1]

	if second.Title != "It's a Wonderful Life; or, Life" {
		t.Errorf("title = %q — a doubled quote, a semicolon and a comma must survive", second.Title)
	}
	if second.Language != "brazillian-portuguese" {
		t.Errorf("language = %q", second.Language)
	}
	if second.ImdbID != "tt0468569" {
		t.Errorf("imdb = %q — a bare integer is still an IMDB id", second.ImdbID)
	}
	if !second.HI {
		t.Error("_HI_ in the file name means hearing impaired")
	}
	if second.Year != 1946 {
		t.Errorf("year = %d, want the year Subscene put in the slug", second.Year)
	}
	if second.Slug != "its-a-wonderful-life-1946" {
		t.Errorf("slug = %q — with no URL column it comes from the directory", second.Slug)
	}
	if second.UploadedAt != "" {
		t.Errorf("uploaded_at = %q, want empty for MySQL's zero date", second.UploadedAt)
	}
	if second.Releases != nil {
		t.Errorf("releases = %v, want none", second.Releases)
	}
}

// The dump is a third-party artefact whose column names are not guaranteed. The
// mapping is worked out from whatever names it has, and reported so an operator
// can check it.
func TestMapping_RecognisesAlternativeColumnNames(t *testing.T) {
	mapping := Mapping([]string{
		"subscene_id", "movie_name", "imdb_link", "lang", "release_names",
		"uploader", "uploader_id", "notes", "upload_date", "zip_file",
	})

	want := map[string]string{
		RoleID:       "subscene_id",
		RoleTitle:    "movie_name",
		RoleIMDB:     "imdb_link",
		RoleLanguage: "lang",
		RoleReleases: "release_names",
		RoleAuthor:   "uploader",
		RoleAuthorID: "uploader_id",
		RoleComment:  "notes",
		RoleDate:     "upload_date",
		RolePath:     "zip_file",
	}
	for role, column := range want {
		if mapping[role] != column {
			t.Errorf("role %s mapped to %q, want %q (full mapping: %v)", role, mapping[role], column, mapping)
		}
	}
}

// `author_id` must not be claimed as the row's own id.
func TestMapping_PrefersTheMoreSpecificRole(t *testing.T) {
	mapping := Mapping([]string{"id", "author_id", "file_path", "title"})

	if mapping[RoleID] != "id" {
		t.Errorf("id mapped to %q", mapping[RoleID])
	}
	if mapping[RoleAuthorID] != "author_id" {
		t.Errorf("author_id mapped to %q", mapping[RoleAuthorID])
	}
}

func TestRead_ColumnListInTheInsert(t *testing.T) {
	source := "INSERT INTO `all_subs` (`id`, `title`, `file_path`) VALUES " +
		"(1,'A','dir/a.zip'),(2,'B','dir/b.zip');\n"

	rows, result := readAll(t, source)
	if result.Table != "all_subs" {
		t.Errorf("table = %q", result.Table)
	}
	if len(rows) != 2 || rows[0].Title != "A" || rows[1].SubsceneID != "2" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestRead_ValuesWithAwkwardCharacters(t *testing.T) {
	source := "CREATE TABLE `all_subs` (`id` int, `title` text, `file_path` text);\n" +
		"INSERT INTO `all_subs` VALUES " +
		`(1,'A title with a ) bracket, a ; semicolon\n and a newline','a/b.zip'),` +
		`(2,'Backslash \\ and quote \'','c/d.zip');` + "\n"

	rows, _ := readAll(t, source)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 — punctuation inside a string must not end it", len(rows))
	}
	if !strings.Contains(rows[0].Title, ") bracket") || !strings.Contains(rows[0].Title, "; semicolon") {
		t.Errorf("title = %q", rows[0].Title)
	}
	if rows[1].Title != `Backslash \ and quote '` {
		t.Errorf("title = %q", rows[1].Title)
	}
}

func TestRead_NoCatalogueTable(t *testing.T) {
	_, err := Read(strings.NewReader("CREATE TABLE `other` (`a` int, `b` int);\n"), func(Upload) error { return nil })
	if err == nil {
		t.Error("expected an error when the dump holds no catalogue")
	}
}

func TestNormaliseIMDB(t *testing.T) {
	tests := []struct{ in, want string }{
		{"tt0468569", "tt0468569"},
		{"468569", "tt0468569"},
		{"0468569", "tt0468569"},
		{"tt12345678", "tt12345678"},
		{"https://www.imdb.com/title/tt0468569/", "tt0468569"},
		{"", ""},
		{"n/a", ""},
		{"tt", ""},
	}
	for _, tc := range tests {
		if got := NormaliseIMDB(tc.in); got != tc.want {
			t.Errorf("NormaliseIMDB(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseReleases(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{`["a","b"]`, 2},
		{"a\nb\nc", 3},
		{"a|b", 2},
		{"a;b", 2},
		{"a,b", 2},
		{"single", 1},
		{"", 0},
		{"   ", 0},
	}
	for _, tc := range tests {
		if got := ParseReleases(tc.in); len(got) != tc.want {
			t.Errorf("ParseReleases(%q) = %v, want %d entries", tc.in, got, tc.want)
		}
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2008-07-20 13:45:00", "2008-07-20T13:45:00Z"},
		{"2008-07-20T13:45:00Z", "2008-07-20T13:45:00Z"},
		{"7/20/2008 1:45 PM", "2008-07-20T13:45:00Z"},
		{"2008-07-20", "2008-07-20T00:00:00Z"},
		{"1216561500", "2008-07-20T13:45:00Z"},
		{"0000-00-00 00:00:00", ""},
		{"", ""},
		{"not a date", ""},
	}
	for _, tc := range tests {
		if got := ParseDate(tc.in); got != tc.want {
			t.Errorf("ParseDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
