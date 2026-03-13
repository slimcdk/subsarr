package pb_migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		existing, _ := app.FindCollectionByNameOrId("subtitles")
		if existing != nil {
			return nil // already applied
		}

		c := core.NewBaseCollection("subtitles")

		// Allow public read access — no auth token needed to list or view records.
		// Write operations (create/update/delete) remain admin-only (nil = locked).
		emptyRule := ""
		c.ListRule = &emptyRule
		c.ViewRule = &emptyRule

		// Subscene metadata
		c.Fields.Add(&core.TextField{Name: "subscene_id", Required: true})
		c.Fields.Add(&core.TextField{Name: "title", Required: true})
		c.Fields.Add(&core.TextField{Name: "slug"})
		c.Fields.Add(&core.TextField{Name: "imdb_id"})
		c.Fields.Add(&core.TextField{Name: "language", Required: true})
		c.Fields.Add(&core.BoolField{Name: "hi"})       // hearing impaired
		c.Fields.Add(&core.TextField{Name: "author"})
		c.Fields.Add(&core.JSONField{Name: "releases"}) // []string of release names
		c.Fields.Add(&core.TextField{Name: "comment"})
		c.Fields.Add(&core.NumberField{Name: "year"})

		// Subtitle file
		c.Fields.Add(&core.TextField{Name: "filename", Required: true})
		c.Fields.Add(&core.TextField{Name: "format"})                    // srt, ass, ssa, sub
		c.Fields.Add(&core.FileField{Name: "content", MaxSize: 20 << 20}) // subtitle file (20 MB max)

		// Tracking
		c.Fields.Add(&core.DateField{Name: "uploaded_at"})
		c.Fields.Add(&core.NumberField{Name: "downloads"})

		c.AddIndex("idx_subtitles_subscene_file", true, "slug, subscene_id, filename", "")
		c.AddIndex("idx_subtitles_imdb_id", false, "imdb_id", "")
		c.AddIndex("idx_subtitles_language", false, "language", "")
		c.AddIndex("idx_subtitles_slug", false, "slug", "")
		c.AddIndex("idx_subtitles_title_lang", false, "title, language", "")

		return app.Save(c)
	}, func(app core.App) error {
		c, _ := app.FindCollectionByNameOrId("subtitles")
		if c != nil {
			return app.Delete(c)
		}
		return nil
	}, "1_init.go")
}
