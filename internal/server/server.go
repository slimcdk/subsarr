package server

import (
	"net/http"

	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
)

type Server struct {
	store   store.Store
	storage storage.Store
}

func New(st store.Store, stor storage.Store) *Server {
	return &Server{store: st, storage: stor}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/info", s.handleInfo)
	mux.HandleFunc("GET /api/v1/languages", s.handleLanguages)
	mux.HandleFunc("GET /api/v1/subtitles/search", s.handleSearch)
	mux.HandleFunc("GET /api/v1/subtitles/{id}/download", s.handleDownload)
	return mux
}
