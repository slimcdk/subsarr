package handlers

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// Register sets up custom API routes on the PocketBase serve event.
func Register(app *pocketbase.PocketBase) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		api := se.Router.Group("/api/v1")

		api.GET("/info", NewInfoHandler(app))
		api.GET("/languages", NewLanguagesHandler(app))
		api.GET("/subtitles/search", NewSearchHandler(app))
		api.GET("/subtitles/{id}/download", NewDownloadHandler(app))

		return se.Next()
	})
}
