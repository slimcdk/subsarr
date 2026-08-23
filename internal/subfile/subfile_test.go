package subfile

import (
	"archive/zip"
	"bytes"
	"testing"
	"unicode/utf8"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

const srt = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"

func TestExtract_Zip(t *testing.T) {
	data := zipOf(t, map[string]string{"The.Dark.Knight.srt": srt})

	files, reason := Extract("whatever.zip", data)
	if reason != ReasonNone {
		t.Fatalf("reason = %q, want none", reason)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Name != "The.Dark.Knight.srt" || files[0].Format != "srt" || string(files[0].Content) != srt {
		t.Errorf("got %+v", files[0])
	}
}

// A zip holding one file per episode has to become one result per episode, or
// Bazarr cannot pick the right one.
func TestExtract_ZipWithSeveralSubtitles(t *testing.T) {
	data := zipOf(t, map[string]string{
		"S02E01.srt": srt,
		"S02E02.srt": srt + "\n",
		"readme.nfo": "not a subtitle",
	})

	files, reason := Extract("season.zip", data)
	if reason != ReasonNone {
		t.Fatalf("reason = %q", reason)
	}
	if len(files) != 2 {
		t.Errorf("got %d files, want 2 (the .nfo is not a subtitle)", len(files))
	}
}

// The format is decided by the bytes, not the name: the dump is full of entries
// whose extension says one thing and whose content says another.
func TestExtract_SignatureBeatsExtension(t *testing.T) {
	data := zipOf(t, map[string]string{"a.srt": srt})

	files, reason := Extract("mislabelled.srt", data)
	if reason != ReasonNone || len(files) != 1 {
		t.Fatalf("a zip named .srt was read as %d files, reason %q", len(files), reason)
	}
}

func TestExtract_RawSubtitleFormats(t *testing.T) {
	tests := []struct {
		name   string
		format string
	}{
		{"a.srt", "srt"},
		{"a.SRT", "srt"},
		{"a.sub", "sub"},
		{"a.ssa", "ssa"},
		{"a.ass", "ass"},
		{"a.smi", "smi"},
		{"a.vtt", "vtt"},
		// Subscene stored MicroDVD under .txt.
		{"a.txt", "microdvd"},
	}
	for _, tc := range tests {
		files, reason := Extract(tc.name, []byte("{1}{2}Hello"))
		if reason != ReasonNone {
			t.Errorf("%s: reason = %q", tc.name, reason)
			continue
		}
		if len(files) != 1 || files[0].Format != tc.format {
			t.Errorf("%s: got %+v, want format %q", tc.name, files, tc.format)
		}
	}
}

func TestExtract_Unusable(t *testing.T) {
	tests := []struct {
		what string
		name string
		data []byte
		want Reason
	}{
		{"zero bytes", "a.zip", nil, ReasonEmpty},
		{"a truncated zip", "a.zip", []byte("PK\x03\x04truncated")[:8], ReasonTruncated},
		{"a JSON error body", "a.zip", []byte(`{"error":"not found","status":404}`), ReasonErrorBody},
		{"an HTML error page", "a.zip", []byte("<!DOCTYPE html><html><body>404</body></html>"), ReasonErrorBody},
		{"a file that is not a subtitle", "a.nfo", []byte("some notes"), ReasonNoSubtitle},
		{"an archive of nothing usable", "a.zip", nil, ReasonEmpty},
	}
	for _, tc := range tests {
		files, reason := Extract(tc.name, tc.data)
		if reason != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.what, reason, tc.want)
		}
		if len(files) != 0 {
			t.Errorf("%s: got %d files, want none", tc.what, len(files))
		}
	}
}

func TestExtract_ZipWithoutSubtitles(t *testing.T) {
	data := zipOf(t, map[string]string{"readme.nfo": "nothing here"})

	files, reason := Extract("a.zip", data)
	if reason != ReasonNoSubtitle || len(files) != 0 {
		t.Errorf("got %d files, reason %q; want none and %q", len(files), reason, ReasonNoSubtitle)
	}
}

func TestExtract_TooLarge(t *testing.T) {
	_, reason := Extract("huge.srt", bytes.Repeat([]byte("x"), MaxFileSize+1))
	if reason != ReasonTooLarge {
		t.Errorf("reason = %q, want %q", reason, ReasonTooLarge)
	}
}

// A subtitle whose text happens to start with a brace must not be mistaken for
// an error body — MicroDVD subtitles start with `{1}{2}`.
func TestExtract_MicroDVDIsNotAnErrorBody(t *testing.T) {
	files, reason := Extract("a.txt", []byte("{1}{60}Hello|World\n{61}{120}Goodbye"))
	if reason != ReasonNone || len(files) != 1 {
		t.Errorf("got %d files, reason %q; want the MicroDVD subtitle", len(files), reason)
	}
}

// A zip written before UTF-8 names were flagged carries the bytes of the code
// page the machine that made it used. The real dump is full of them: 4.5 % of the
// Danish files, and every one of them is a row PostgreSQL and MySQL refuse.
func TestExtract_NamesThatAreNotUTF8(t *testing.T) {
	// `\xC4nglavakt DANSK.srt` — Änglavakt, as Windows wrote it.
	raw := "\xC4nglavakt DANSK.srt"
	if utf8.ValidString(raw) {
		t.Fatal("the fixture is supposed to be invalid UTF-8")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(srt))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	files, reason := Extract("a.zip", buf.Bytes())
	if reason != ReasonNone || len(files) != 1 {
		t.Fatalf("got %d files, reason %q", len(files), reason)
	}
	if !utf8.ValidString(files[0].Name) {
		t.Errorf("name %q is not valid UTF-8; a database will refuse the row", files[0].Name)
	}
	if files[0].Name != "Änglavakt DANSK.srt" {
		t.Errorf("name = %q, want %q", files[0].Name, "Änglavakt DANSK.srt")
	}
}

// A name that is already UTF-8 must survive untouched, including one that only
// looks like it needs decoding.
func TestExtract_NamesThatAreUTF8AreLeftAlone(t *testing.T) {
	for _, name := range []string{
		"Änglavakt DANSK.srt",
		"日本語字幕.srt",
		"Ünnepi.srt",
		"plain.srt",
	} {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(name)
		w.Write([]byte(srt))
		zw.Close()

		files, reason := Extract("a.zip", buf.Bytes())
		if reason != ReasonNone || len(files) != 1 {
			t.Fatalf("%s: got %d files, reason %q", name, len(files), reason)
		}
		if files[0].Name != name {
			t.Errorf("name = %q, want it unchanged as %q", files[0].Name, name)
		}
	}
}
