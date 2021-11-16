// Developing a RESTful API with Go and ... Go
//
// This is a rewrite of https://golang.org/doc/tutorial/web-service-gin
// using just the Go standard library (and fixing a few issues).

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
)

func main() {
	// Allow user to specify listen port on command line
	var port int
	flag.IntVar(&port, "port", 8080, "port to listen on")
	flag.Parse()

	// Create in-memory database and add a couple of test albums
	db := NewMemoryDatabase()
	db.AddAlbum(Album{ID: "a1", Title: "9th Symphony", Artist: "Beethoven", Price: 795})
	db.AddAlbum(Album{ID: "a2", Title: "Hey Jude", Artist: "The Beatles", Price: 2000})

	// Create server and wire up database
	server := NewServer(db, log.Default())

	log.Printf("listening on http://localhost:%d", port)
	http.ListenAndServe(":"+strconv.Itoa(port), server)
}

// Server is the album HTTP server.
type Server struct {
	db  Database
	log *log.Logger
}

// Database is the interface used by the server to load and store albums.
type Database interface {
	// GetAlbums returns a copy of all albums, sorted by ID.
	GetAlbums() ([]Album, error)

	// GetAlbumsByID returns a single album by ID, or ErrDoesNotExist if
	// an album with that ID does not exist.
	GetAlbumByID(id string) (Album, error)

	// AddAlbum adds a single album, or ErrAlreadyExists if an album with
	// the given ID already exists.
	AddAlbum(album Album) error
}

var (
	ErrDoesNotExist  = errors.New("does not exist")
	ErrAlreadyExists = errors.New("already exists")
)

const (
	ErrorAlreadyExists    = "already-exists"
	ErrorDatabase         = "database"
	ErrorInternal         = "internal"
	ErrorMalformedJSON    = "malformed-json"
	ErrorMethodNotAllowed = "method-not-allowed"
	ErrorNotFound         = "not-found"
	ErrorValidation       = "validation"
)

// Album represents data about a single album.
type Album struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Price  int    `json:"price,omitempty"` // use int cents instead of float64 for currency
}

// NewServer creates a new server using the given database implementation.
func NewServer(db Database, log *log.Logger) *Server {
	return &Server{db: db, log: log}
}

// Regex to match "/albums/:id" (id must be one or more non-slash chars).
var reAlbumsID = regexp.MustCompile(`^/albums/([^/]+)$`)

// ServeHTTP routes the request and calls the correct handler based on the URL
// and HTTP method. It writes a 404 Not Found if the request URL is unknown,
// or 405 Method Not Allowed if the request method is invalid.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	s.log.Printf("%s %s", r.Method, path)

	var id string

	switch {
	case path == "/albums":
		switch r.Method {
		case "GET":
			s.getAlbums(w, r)
		case "POST":
			s.addAlbum(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			s.jsonError(w, http.StatusMethodNotAllowed, ErrorMethodNotAllowed, nil)
		}

	case match(path, reAlbumsID, &id):
		switch r.Method {
		case "GET":
			s.getAlbumByID(w, r, id)
		default:
			w.Header().Set("Allow", "GET")
			s.jsonError(w, http.StatusMethodNotAllowed, ErrorMethodNotAllowed, nil)
		}

	default:
		s.jsonError(w, http.StatusNotFound, ErrorNotFound, nil)
	}
}

// match returns true if path matches the regex pattern, and binds any
// capturing groups in pattern to the vars.
func match(path string, pattern *regexp.Regexp, vars ...*string) bool {
	matches := pattern.FindStringSubmatch(path)
	if len(matches) <= 0 {
		return false
	}
	for i, match := range matches[1:] {
		*vars[i] = match
	}
	return true
}

func (s *Server) getAlbums(w http.ResponseWriter, r *http.Request) {
	albums, err := s.db.GetAlbums()
	if err != nil {
		s.log.Printf("error fetching albums: %v", err)
		s.jsonError(w, http.StatusInternalServerError, ErrorDatabase, nil)
		return
	}
	s.writeJSON(w, http.StatusOK, albums)
}

func (s *Server) addAlbum(w http.ResponseWriter, r *http.Request) {
	var album Album
	if !s.readJSON(w, r, &album) {
		return
	}

	// Validate the input and build a map of validation issues
	type validationIssue struct {
		Error   string `json:"error"`
		Message string `json:"message,omitempty"`
	}
	issues := make(map[string]interface{})
	if album.ID == "" {
		issues["id"] = validationIssue{"required", ""}
	}
	if album.Title == "" {
		issues["title"] = validationIssue{"required", ""}
	}
	if album.Artist == "" {
		issues["artist"] = validationIssue{"required", ""}
	}
	if album.Price < 0 || album.Price >= 100000 {
		issues["price"] = validationIssue{"out-of-range", "price must be between 0 and $1000"}
	}
	if len(issues) > 0 {
		s.jsonError(w, http.StatusBadRequest, ErrorValidation, issues)
		return
	}

	err := s.db.AddAlbum(album)
	if errors.Is(err, ErrAlreadyExists) {
		s.jsonError(w, http.StatusConflict, ErrorAlreadyExists, nil)
		return
	} else if err != nil {
		s.log.Printf("error adding album ID %q: %v", album.ID, err)
		s.jsonError(w, http.StatusInternalServerError, ErrorDatabase, nil)
		return
	}

	s.writeJSON(w, http.StatusCreated, album)
}

func (s *Server) getAlbumByID(w http.ResponseWriter, r *http.Request, id string) {
	album, err := s.db.GetAlbumByID(id)
	if errors.Is(err, ErrDoesNotExist) {
		s.jsonError(w, http.StatusNotFound, ErrorNotFound, nil)
		return
	} else if err != nil {
		s.log.Printf("error fetching album ID %q: %v", id, err)
		s.jsonError(w, http.StatusInternalServerError, ErrorDatabase, nil)
		return
	}
	s.writeJSON(w, http.StatusOK, album)
}

// writeJSON marshals v to JSON and writes it to the response, handling
// errors as appropriate. It also sets the Content-Type header to
// "application/json".
func (s *Server) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	b, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		s.log.Printf("error marshaling JSON: %v", err)
		http.Error(w, `{"error":"`+ErrorInternal+`"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, err = w.Write(b)
	if err != nil {
		// Very unlikely to happen, but log any error (not much more we can do)
		s.log.Printf("error writing JSON: %v", err)
	}
}

// jsonError writes a structured error as JSON to the response, with
// optional structured data in the "data" field.
func (s *Server) jsonError(w http.ResponseWriter, status int, error string, data map[string]interface{}) {
	response := struct {
		Status int                    `json:"status"`
		Error  string                 `json:"error"`
		Data   map[string]interface{} `json:"data,omitempty"`
	}{
		Status: status,
		Error:  error,
		Data:   data,
	}
	s.writeJSON(w, status, response)
}

// readJSON reads the request body and unmarshals it from JSON, handling
// errors as appropriate. It returns true on success; the caller should
// return from the handler early if it returns false.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		s.log.Printf("error reading JSON body: %v", err)
		s.jsonError(w, http.StatusInternalServerError, ErrorInternal, nil)
		return false
	}
	err = json.Unmarshal(b, v)
	if err != nil {
		data := map[string]interface{}{"message": err.Error()}
		s.jsonError(w, http.StatusBadRequest, ErrorMalformedJSON, data)
		return false
	}
	return true
}

// MemoryDatabase is a Database implementation that uses a simple
// in-memory map to store the albums.
type MemoryDatabase struct {
	lock   sync.RWMutex
	albums map[string]Album
}

// NewMemoryDatabase creates a new in-memory database.
func NewMemoryDatabase() *MemoryDatabase {
	return &MemoryDatabase{albums: make(map[string]Album)}
}

func (d *MemoryDatabase) GetAlbums() ([]Album, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// Make a copy of the albums map (as a slice)
	albums := make([]Album, 0, len(d.albums))
	for _, album := range d.albums {
		albums = append(albums, album)
	}

	// Sort by ID so we return them in a defined order
	sort.Slice(albums, func(i, j int) bool {
		return albums[i].ID < albums[j].ID
	})
	return albums, nil
}

func (d *MemoryDatabase) GetAlbumByID(id string) (Album, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	album, ok := d.albums[id]
	if !ok {
		return Album{}, ErrDoesNotExist
	}
	return album, nil
}

func (d *MemoryDatabase) AddAlbum(album Album) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	if _, ok := d.albums[album.ID]; ok {
		return ErrAlreadyExists
	}
	d.albums[album.ID] = album
	return nil
}
