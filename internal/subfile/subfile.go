// Package subfile decides what an archive entry actually holds and pulls the
// subtitle files out of it.
//
// The dump's entries are not all zips. About four per cent are RAR archives or
// raw subtitle files, and a further slice are not subtitles at all: downloads
// that were truncated when the archive was collected, and JSON or HTML error
// bodies that a scraper saved under a .zip name. Deciding by content signature
// rather than by extension is what tells those apart.
package subfile

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

// MaxFileSize is the largest subtitle file that will be stored. Anything larger
// is not a subtitle; the biggest real ones are a few hundred kilobytes.
const MaxFileSize = 20 << 20

// MaxEntrySize is the largest archive entry that will be opened. An entry is an
// archive of its own and can legitimately hold a whole season, so the cap is far
// above MaxFileSize — it exists to stop one pathological entry from being read
// into memory, not to filter anything real.
const MaxEntrySize = 256 << 20

// File is one subtitle file extracted from an entry.
type File struct {
	Name    string
	Format  string
	Content []byte
}

// Reason says why an entry produced no subtitle file. It is recorded per entry so
// that a missing row can always be explained.
type Reason string

const (
	ReasonNone       Reason = ""
	ReasonEmpty      Reason = "empty"       // zero bytes
	ReasonTruncated  Reason = "truncated"   // an archive that will not open
	ReasonErrorBody  Reason = "error_body"  // a JSON or HTML error saved as a download
	ReasonNoSubtitle Reason = "no_subtitle" // an archive holding nothing recognisable
	ReasonTooLarge   Reason = "too_large"   // over MaxFileSize
	ReasonUnreadable Reason = "unreadable"  // the entry itself could not be read
)

// subtitleExtensions are the formats Bazarr can use. `.txt` is included because
// Subscene stored MicroDVD subtitles under it.
var subtitleExtensions = map[string]string{
	".srt": "srt",
	".sub": "sub",
	".ssa": "ssa",
	".ass": "ass",
	".smi": "smi",
	".vtt": "vtt",
	".txt": "microdvd",
}

// Format returns the subtitle format for a file name, and whether the name is a
// subtitle at all.
func Format(name string) (string, bool) {
	format, ok := subtitleExtensions[strings.ToLower(path.Ext(name))]
	return format, ok
}

// Extract returns the subtitle files an entry holds.
//
// name is only used to decide the format of a raw subtitle file; what the entry
// *is* comes from its bytes.
func Extract(name string, data []byte) ([]File, Reason) {
	switch {
	case len(data) == 0:
		return nil, ReasonEmpty

	case isZip(data):
		files, err := fromZip(data)
		if err != nil {
			return nil, ReasonTruncated
		}
		if len(files) == 0 {
			return nil, ReasonNoSubtitle
		}
		return files, ReasonNone

	case isRAR(data):
		files, err := fromRAR(data)
		if err != nil {
			return nil, ReasonTruncated
		}
		if len(files) == 0 {
			return nil, ReasonNoSubtitle
		}
		return files, ReasonNone

	case isErrorBody(data):
		return nil, ReasonErrorBody
	}

	format, ok := Format(name)
	if !ok {
		return nil, ReasonNoSubtitle
	}
	if len(data) > MaxFileSize {
		return nil, ReasonTooLarge
	}
	return []File{{Name: path.Base(name), Format: format, Content: data}}, ReasonNone
}

func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' &&
		(data[2] == 3 || data[2] == 5 || data[2] == 7)
}

// isRAR covers both the 1.5–4.x signature and the 5.0 one.
func isRAR(data []byte) bool {
	return bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x00")) ||
		bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x01\x00"))
}

// isErrorBody recognises the pages a scraper saved instead of a subtitle: an API
// error as JSON, or a site's HTML. They are perfectly valid files, which is why
// only their content gives them away.
func isErrorBody(data []byte) bool {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	trimmed = bytes.TrimPrefix(trimmed, []byte("\xef\xbb\xbf")) // a UTF-8 byte order mark
	if len(trimmed) == 0 {
		return true
	}
	switch trimmed[0] {
	case '{', '[':
		var v any
		return json.Unmarshal(trimmed, &v) == nil
	case '<':
		lower := bytes.ToLower(trimmed[:min(len(trimmed), 512)])
		return bytes.Contains(lower, []byte("<html")) ||
			bytes.Contains(lower, []byte("<!doctype")) ||
			bytes.Contains(lower, []byte("<?xml"))
	}
	return false
}

func fromZip(data []byte) ([]File, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}

	var files []File
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		format, ok := Format(zf.Name)
		if !ok {
			continue
		}
		if zf.UncompressedSize64 > MaxFileSize {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			// One unreadable member does not condemn the others.
			continue
		}
		content, err := io.ReadAll(io.LimitReader(rc, MaxFileSize+1))
		rc.Close()
		if err != nil || len(content) == 0 || len(content) > MaxFileSize {
			continue
		}
		files = append(files, File{Name: path.Base(zf.Name), Format: format, Content: content})
	}
	return files, nil
}

func fromRAR(data []byte) ([]File, error) {
	r, err := rardecode.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	var files []File
	for {
		header, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A RAR that fails partway still yields what was read before it.
			if len(files) > 0 {
				break
			}
			return nil, err
		}
		if header.IsDir {
			continue
		}
		format, ok := Format(header.Name)
		if !ok {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(r, MaxFileSize+1))
		if err != nil || len(content) == 0 || len(content) > MaxFileSize {
			continue
		}
		files = append(files, File{Name: path.Base(header.Name), Format: format, Content: content})
	}
	return files, nil
}
