package importcmd

import (
	"archive/zip"
	"bytes"
	"testing"
	"time"
)

// ─── parseV2Filename ──────────────────────────────────────────────────────────

func TestParseV2Filename(t *testing.T) {
	tests := []struct {
		filename   string
		wantID     string
		wantLang   string
		wantHI     bool
	}{
		{
			filename: "the-dark-knight_English-3193694.zip",
			wantID:   "3193694",
			wantLang: "English",
			wantHI:   false,
		},
		{
			filename: "the-dark-knight_HI_English-3193694.zip",
			wantID:   "3193694",
			wantLang: "English",
			wantHI:   true,
		},
		{
			filename: "loki-second-season_Greek-4219876.zip",
			wantID:   "4219876",
			wantLang: "Greek",
			wantHI:   false,
		},
		// Language with spaces/hyphens uses the last dash as separator
		{
			filename: "some-show_Brazilian-Portuguese-999.zip",
			wantID:   "999",
			wantLang: "Brazilian-Portuguese",
			wantHI:   false,
		},
		// No underscore → unparseable
		{filename: "nodash.zip", wantID: "", wantLang: "", wantHI: false},
		// No language-id separator
		{filename: "show_nolangid.zip", wantID: "", wantLang: "", wantHI: false},
		// Empty language
		{filename: "show_-123.zip", wantID: "", wantLang: "", wantHI: false},
		// Empty id
		{filename: "show_English-.zip", wantID: "", wantLang: "", wantHI: false},
	}

	for _, tt := range tests {
		id, lang, hi := parseV2Filename("ignored-slug", tt.filename)
		if id != tt.wantID || lang != tt.wantLang || hi != tt.wantHI {
			t.Errorf("parseV2Filename(%q): got (%q, %q, %v), want (%q, %q, %v)",
				tt.filename, id, lang, hi, tt.wantID, tt.wantLang, tt.wantHI)
		}
	}
}

// ─── storageFilename ──────────────────────────────────────────────────────────

func TestStorageFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"subtitle.srt", "subtitle.srt"},
		{"My.Subtitle.srt", "my_subtitle.srt"},
		{"A.History.of.Violence.XviD-UNDEAD.srt", "a_history_of_violence_xvid_undead.srt"},
		{"Show.S01E05.1080p.BluRay.srt", "show_s01e05_1080p_bluray.srt"},
		// Leading/trailing separators trimmed from base
		{"---subtitle---.srt", "subtitle.srt"},
		// Uppercase extension normalised
		{"file.SRT", "file.srt"},
		{"file.ASS", "file.ass"},
		// Empty base after normalisation → "subtitle"
		{"---.srt", "subtitle.srt"},
		{"   .srt", "subtitle.srt"},
		// No extension
		{"plainname", "plainname"},
	}

	for _, tt := range tests {
		got := storageFilename(tt.input)
		if got != tt.want {
			t.Errorf("storageFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ─── slugToTitle ──────────────────────────────────────────────────────────────

func TestSlugToTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"the-dark-knight-rises", "The Dark Knight Rises"},
		{"loki", "Loki"},
		{"a-history-of-violence", "A History Of Violence"},
		{"", ""},
	}

	for _, tt := range tests {
		got := slugToTitle(tt.input)
		if got != tt.want {
			t.Errorf("slugToTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ─── extractSlug ─────────────────────────────────────────────────────────────

func TestExtractSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/subtitles/the-dark-knight/english", "the-dark-knight"},
		{"/subtitles/loki/", "loki"},
		{"/subtitles/the-dark-knight", "the-dark-knight"},
		{"no-subtitles-prefix", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := extractSlug(tt.input)
		if got != tt.want {
			t.Errorf("extractSlug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ─── extractIMDB ─────────────────────────────────────────────────────────────

func TestExtractIMDB(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://www.imdb.com/title/tt0468569/", "tt0468569"},
		{"https://www.imdb.com/title/tt1234567", "tt1234567"},
		{"https://www.imdb.com/title/tt0000001/?ref_=nv_sr_srsg_0", "tt0000001"},
		{"not-a-url", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := extractIMDB(tt.input)
		if got != tt.want {
			t.Errorf("extractIMDB(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ─── parseDate ───────────────────────────────────────────────────────────────

func TestParseDate(t *testing.T) {
	tests := []struct {
		input   string
		wantUTC time.Time
	}{
		{
			"1/15/2021 3:04 PM",
			time.Date(2021, 1, 15, 15, 4, 0, 0, time.UTC),
		},
		{
			"12/31/2019 11:59 PM",
			time.Date(2019, 12, 31, 23, 59, 0, 0, time.UTC),
		},
		{"", time.Time{}},
		{"not-a-date", time.Time{}},
	}

	for _, tt := range tests {
		got := parseDate(tt.input)
		if !got.Equal(tt.wantUTC) {
			t.Errorf("parseDate(%q) = %v, want %v", tt.input, got, tt.wantUTC)
		}
	}
}

// ─── isUniqueErr ─────────────────────────────────────────────────────────────

func TestIsUniqueErr(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"UNIQUE constraint failed: subtitles.subscene_id", true},
		{"Value must be unique", true},
		{"some other error", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isUniqueErr(tt.msg)
		if got != tt.want {
			t.Errorf("isUniqueErr(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

// ─── extractSubtitlesFromZIPBytes ────────────────────────────────────────────

func makeZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
	}
	w.Close()
	return buf.Bytes()
}

func TestExtractSubtitlesFromZIPBytes(t *testing.T) {
	t.Run("extracts srt and ass files", func(t *testing.T) {
		data := makeZIP(t, map[string]string{
			"sub.srt":    "1\n00:00:01,000 --> 00:00:02,000\nHello",
			"sub.ass":    "[Script Info]",
			"readme.txt": "ignore me",
			"cover.jpg":  "ignore me",
		})
		files := extractSubtitlesFromZIPBytes(data)
		if len(files) != 2 {
			t.Fatalf("got %d files, want 2", len(files))
		}
		formats := map[string]bool{}
		for _, f := range files {
			formats[f.format] = true
		}
		if !formats["srt"] || !formats["ass"] {
			t.Errorf("expected srt and ass, got %v", formats)
		}
	})

	t.Run("ignores non-subtitle files", func(t *testing.T) {
		data := makeZIP(t, map[string]string{
			"readme.txt": "not a subtitle",
			"image.png":  "not a subtitle",
		})
		files := extractSubtitlesFromZIPBytes(data)
		if len(files) != 0 {
			t.Errorf("got %d files, want 0", len(files))
		}
	})

	t.Run("handles empty zip", func(t *testing.T) {
		data := makeZIP(t, map[string]string{})
		files := extractSubtitlesFromZIPBytes(data)
		if len(files) != 0 {
			t.Errorf("got %d files, want 0", len(files))
		}
	})

	t.Run("returns nil on invalid data", func(t *testing.T) {
		files := extractSubtitlesFromZIPBytes([]byte("not a zip"))
		if files != nil {
			t.Errorf("expected nil, got %v", files)
		}
	})

	t.Run("filename preserved without directory prefix", func(t *testing.T) {
		data := makeZIP(t, map[string]string{
			"subdir/episode.srt": "content",
		})
		files := extractSubtitlesFromZIPBytes(data)
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		if files[0].filename != "episode.srt" {
			t.Errorf("filename = %q, want %q", files[0].filename, "episode.srt")
		}
	})
}
