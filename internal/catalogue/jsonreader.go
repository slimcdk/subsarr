package catalogue

import (
	"encoding/json"
	"fmt"
	"html"
	"io"

	"github.com/slimcdk/subsarr/internal/lang"
)

// v1Entry is one object in the "Subscene Final" dump's metadata.json — the older
// dump's catalogue. It carries the same facts as the SQL one under different
// names, so it is read into the same Upload and imported by the same code.
type v1Entry struct {
	SubsceneID string   `json:"subscene_id"`
	Title      string   `json:"title"`
	Language   string   `json:"language"`
	Author     string   `json:"author"`
	Releases   []string `json:"releases"`
	Comment    string   `json:"comment"`
	Download   string   `json:"download"`
	Original   string   `json:"original"`
	IMDB       string   `json:"imdb"`
	Date       string   `json:"date"`
}

// ReadJSON streams a metadata.json catalogue.
//
// The array is decoded entry by entry: the file holds millions of objects and
// will not fit in memory as a whole.
func ReadJSON(r io.Reader, fn func(Upload) error) (Result, error) {
	result := Result{Table: "metadata.json", Mapping: map[string]string{}}

	decoder := json.NewDecoder(r)
	if _, err := decoder.Token(); err != nil {
		return result, fmt.Errorf("metadata.json is not a JSON array: %w", err)
	}

	for decoder.More() {
		var entry v1Entry
		if err := decoder.Decode(&entry); err != nil {
			result.Skipped++
			continue
		}
		upload := uploadFromV1(entry)
		if upload.SubsceneID == "" {
			result.Skipped++
			continue
		}
		result.Rows++
		if err := fn(upload); err != nil {
			return result, err
		}
	}
	return result, nil
}

func uploadFromV1(entry v1Entry) Upload {
	return complete(Upload{
		SubsceneID: entry.SubsceneID,
		// The download name is the file's name inside the dump's subtitles/
		// directory; it is what an archive entry is matched against.
		FilePath:   entry.Download,
		Slug:       entry.Original,
		Title:      entry.Title,
		ImdbID:     NormaliseIMDB(entry.IMDB),
		Language:   lang.Canonical(entry.Language),
		Author:     html.UnescapeString(entry.Author),
		Comment:    html.UnescapeString(entry.Comment),
		Releases:   clean(entry.Releases),
		UploadedAt: ParseDate(entry.Date),
	})
}
