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
	"strings"
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
	db.AddAlbum(Album{ID: "a2", Title: "Hey Jude", Artist: "The Beetles", Price: 2000})

	// Create server and wire up database
	server := NewServer(db)

	log.Printf("listening on http://localhost:%d", port)
	http.ListenAndServe(":"+strconv.Itoa(port), server)
}

// Server is the album server.
type Server struct {
	db Database
}

// Database is the interface used by the server to load and store albums.
type Database interface {
	// GetAlbums returns a copy of all albums in the database, sorted by ID.
	GetAlbums() ([]Album, error)

	// GetAlbumsByID returns a single album by ID, or ErrDoesNotExist if an
	// album with that ID does not exist.
	GetAlbumByID(id string) (Album, error)

	// AddAlbum adds a single album, or ErrAlreadyExists if an album with the
	// given ID already exists.
	AddAlbum(album Album) error
}

var (
	ErrDoesNotExist  = errors.New("does not exist")
	ErrAlreadyExists = errors.New("already exists")
)

// Album represents data about a single album.
type Album struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Price  int    `json:"price"` // not using floating point for currency
}

// NewServer creates a new server using the given database implementation.
func NewServer(db Database) *Server {
	return &Server{db: db}
}

// Regex to match "/albums/:id" (id must be one or more non-slash chars).
var albumsIDRegexp = regexp.MustCompile(`^/albums/[^/]+$`)

// ServeHTTP routes the request and calls the correct handler based on the URL
// and HTTP method. It writes a 404 Not Found if the request URL is unknown,
// or 405 Method Not Allowed if the request method is invalid.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	log.Printf("%s %s", r.Method, path)

	switch {
	case path == "/albums":
		switch r.Method {
		case "GET":
			s.getAlbums(w, r)
		case "POST":
			s.addAlbum(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

	case albumsIDRegexp.MatchString(path):
		switch r.Method {
		case "GET":
			id := path[len("/albums/"):]
			s.getAlbumByID(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) getAlbums(w http.ResponseWriter, r *http.Request) {
	albums, err := s.db.GetAlbums()
	if err != nil {
		http.Error(w, "error fetching albums", http.StatusInternalServerError)
		return
	}
	writeJSON(w, albums)
}

func (s *Server) addAlbum(w http.ResponseWriter, r *http.Request) {
	var album Album
	if !readJSON(w, r, &album) {
		return
	}

	// Validate the input (simplistic: just check required fields are present)
	var missing []string
	if album.ID == "" {
		missing = append(missing, "id")
	}
	if album.Title == "" {
		missing = append(missing, "title")
	}
	if album.Artist == "" {
		missing = append(missing, "artist")
	}
	if len(missing) > 0 {
		http.Error(w, "missing fields: "+strings.Join(missing, ", "), http.StatusBadRequest)
		return
	}

	err := s.db.AddAlbum(album)
	if err == ErrAlreadyExists {
		http.Error(w, "album "+album.ID+" already exists", http.StatusConflict)
		return
	} else if err != nil {
		http.Error(w, "error adding album", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, album)
}

func (s *Server) getAlbumByID(w http.ResponseWriter, r *http.Request, id string) {
	album, err := s.db.GetAlbumByID(id)
	if err == ErrDoesNotExist {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "error fetching album", http.StatusInternalServerError)
		return
	}
	writeJSON(w, album)
}

// writeJSON marshals v to JSON and writes it to the response, handling errors
// as appropriate. It also sets the Content-Type header to application/json.
func writeJSON(w http.ResponseWriter, v interface{}) {
	b, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		http.Error(w, "error marshaling JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// readJSON reads the request body and unmarshals it from JSON, handling
// errors as appropriate. It returns true on success; the caller should return
// from the handler early if it returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "error reading body", http.StatusInternalServerError)
		return false
	}
	err = json.Unmarshal(b, v)
	if err != nil {
		http.Error(w, "error unmarshaling JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// MemoryDatabase is a Database implementation that uses a simple in-memory map
// to store the albums.
type MemoryDatabase struct {
	lock   sync.Mutex
	albums map[string]Album
}

// NewMemoryDatabase creates a new in-memory database.
func NewMemoryDatabase() *MemoryDatabase {
	return &MemoryDatabase{albums: make(map[string]Album)}
}

func (d *MemoryDatabase) GetAlbums() ([]Album, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

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
	d.lock.Lock()
	defer d.lock.Unlock()

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
