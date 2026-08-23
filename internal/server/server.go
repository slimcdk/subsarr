// Package server is subsarr's HTTP API.
//
// The contract is frozen: Bazarr's subsarr provider is a released client that
// this service must keep answering. What the handlers may change is whether the
// answers are honest — features reported from the data, exact totals, arrays
// that are always arrays, and a 400 only for input that is genuinely malformed.
package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
)

// catalogueTTL bounds how long the cached view of the catalogue may lag behind an
// import running in another process.
const catalogueTTL = 15 * time.Minute

type Server struct {
	store   store.Store
	storage storage.Store
	version string

	// The catalogue view aggregates every row in the database, so it is far too
	// expensive to recompute per request and only changes on import.
	mu   sync.Mutex
	view *catalogueView
}

// catalogueView is what the API knows about the data as a whole.
type catalogueView struct {
	languages []languageEntry
	hasIMDB   bool
	expires   time.Time
}

func New(st store.Store, stor storage.Store, version string) *Server {
	if version == "" {
		version = "dev"
	}
	return &Server{store: st, storage: stor, version: version}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/info", s.handleInfo)
	mux.HandleFunc("GET /api/v1/languages", s.handleLanguages)
	mux.HandleFunc("GET /api/v1/subtitles/search", s.handleSearch)
	mux.HandleFunc("GET /api/v1/subtitles/{id}/download", s.handleDownload)
	mux.HandleFunc("GET /api/v1/openapi.yaml", s.handleOpenAPI)
	return mux
}

// Warm loads the catalogue view before the first request. Doing it lazily would
// make whichever request arrives first — usually Bazarr's — pay for a full-table
// aggregate over millions of rows.
func (s *Server) Warm(ctx context.Context) error {
	_, err := s.loadCatalogue(ctx)
	return err
}

// loadCatalogue returns the cached view, recomputing it when stale. The lock is
// held across the queries on purpose: concurrent misses would otherwise each run
// the same expensive aggregate.
func (s *Server) loadCatalogue(ctx context.Context) (*catalogueView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.view != nil && time.Now().Before(s.view.expires) {
		return s.view, nil
	}

	rows, err := s.store.ListLanguages(ctx)
	if err != nil {
		return nil, err
	}
	hasIMDB, err := s.store.HasIMDBIDs(ctx)
	if err != nil {
		return nil, err
	}

	languages := make([]languageEntry, 0, len(rows))
	for _, row := range rows {
		languages = append(languages, languageEntry{Name: row.Language, Count: row.Count})
	}

	s.view = &catalogueView{
		languages: languages,
		hasIMDB:   hasIMDB,
		expires:   time.Now().Add(catalogueTTL),
	}
	return s.view, nil
}
