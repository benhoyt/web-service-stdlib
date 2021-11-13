// Tests for the server functions

package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Duplicate this struct in tests so tests catch breaking changes.
type testAlbum struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Price  int    `json:"price"`
}

func TestGetAlbums(t *testing.T) {
	server := newTestServer()
	result := serve(t, server, newRequest(t, "GET", "/albums", nil))
	ensureStatusCode(t, result, http.StatusOK)

	var albums []testAlbum
	unmarshalResponse(t, result.Body, &albums)
	expected := []testAlbum{
		{ID: "a1", Title: "9th Symphony", Artist: "Beethoven", Price: 795},
		{ID: "a2", Title: "Hey Jude", Artist: "The Beetles", Price: 2000},
	}
	if !reflect.DeepEqual(albums, expected) {
		t.Fatalf("bad response: got vs want:\n%#v\n%#v", albums, expected)
	}
}

func TestGetAlbum(t *testing.T) {
	server := newTestServer()

	tests := []getAlbumTest{
		{"/albums/", http.StatusNotFound, testAlbum{}},
		{"/albums/a1", http.StatusOK, testAlbum{ID: "a1", Title: "9th Symphony", Artist: "Beethoven", Price: 795}},
		{"/albums/a2", http.StatusOK, testAlbum{ID: "a2", Title: "Hey Jude", Artist: "The Beetles", Price: 2000}},
		{"/albums/a3", http.StatusNotFound, testAlbum{}},
		{"/albums/foo/bar", http.StatusNotFound, testAlbum{}},
	}
	for _, test := range tests {
		t.Run(test.path[1:], func(t *testing.T) {
			testGetAlbum(t, server, test)
		})
	}
}

type getAlbumTest struct {
	path     string
	code     int
	expected testAlbum
}

// This test logic is factored out as it's used in a few places.
func testGetAlbum(t *testing.T, server *Server, test getAlbumTest) {
	result := serve(t, server, newRequest(t, "GET", test.path, nil))
	ensureStatusCode(t, result, test.code)
	if test.code != http.StatusOK {
		return
	}

	var album testAlbum
	unmarshalResponse(t, result.Body, &album)
	if !reflect.DeepEqual(album, test.expected) {
		t.Fatalf("bad response: got vs want:\n%#v\n%#v", album, test.expected)
	}
}

func TestAddAlbumCreated(t *testing.T) {
	server := newTestServer()
	body := `{"id": "a9", "title": "Pianoman", "artist": "Billy Joel", "price": 1234}`
	result := serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(body)))
	ensureStatusCode(t, result, http.StatusCreated)

	var album testAlbum
	unmarshalResponse(t, result.Body, &album)
	expected := testAlbum{ID: "a9", Title: "Pianoman", Artist: "Billy Joel", Price: 1234}
	if !reflect.DeepEqual(album, expected) {
		t.Fatalf("bad response: got vs want:\n%#v\n%#v", album, expected)
	}

	// Ensure we can fetch the album after it's been created
	testGetAlbum(t, server, getAlbumTest{"/albums/a9", http.StatusOK, expected})

	// Ensure /albums lists the new album
	result = serve(t, server, newRequest(t, "GET", "/albums", nil))
	ensureStatusCode(t, result, http.StatusOK)
	var albums []testAlbum
	unmarshalResponse(t, result.Body, &albums)
	for _, album := range albums {
		if album.ID == "a9" {
			if !reflect.DeepEqual(album, expected) {
				t.Fatalf("bad response: got vs want:\n%#v\n%#v", album, expected)
			}
			return
		}
	}
	t.Fatalf("new album not in albums list:\n%#v", albums)
}

func TestAddAlbumAlreadyExists(t *testing.T) {
	server := newTestServer()
	body := `{"id": "a2", "title": "Foo", "artist": "Bar"}`
	result := serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(body)))
	ensureStatusCode(t, result, http.StatusConflict)

	// Ensure it didn't modify the album
	expected := testAlbum{ID: "a2", Title: "Hey Jude", Artist: "The Beetles", Price: 2000}
	testGetAlbum(t, server, getAlbumTest{"/albums/a2", http.StatusOK, expected})
}

func TestAddAlbumBadJSON(t *testing.T) {
	server := newTestServer()
	result := serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader("@")))
	ensureStatusCode(t, result, http.StatusBadRequest)
}

func TestAddAlbumMissingFields(t *testing.T) {
	server := newTestServer()
	result := serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader("{}")))
	ensureStatusCode(t, result, http.StatusBadRequest)
}

func TestConcurrentRequests(t *testing.T) {
	server := newTestServer()
	for i := 0; i < 100; i++ {
		go func(i int) {
			result := serve(t, server, newRequest(t, "GET", "/albums", nil))
			ensureStatusCode(t, result, http.StatusOK)

			albumID := "c" + strconv.Itoa(i)
			body := `{"id": "` + albumID + `", "title": "T", "artist": "A"}`
			result = serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(body)))
			ensureStatusCode(t, result, http.StatusCreated)

			result = serve(t, server, newRequest(t, "GET", "/albums/"+albumID, nil))
			ensureStatusCode(t, result, http.StatusOK)
		}(i)
	}
}

func TestDatabaseErrors(t *testing.T) {
	db := errorDatabase{}
	server := NewServer(db, log.New(io.Discard, "", 0))

	result := serve(t, server, newRequest(t, "GET", "/albums", nil))
	ensureStatusCode(t, result, http.StatusInternalServerError)

	body := `{"id": "a9", "title": "Pianoman", "artist": "Billy Joel"}`
	result = serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(body)))
	ensureStatusCode(t, result, http.StatusInternalServerError)

	result = serve(t, server, newRequest(t, "GET", "/albums/a1", nil))
	ensureStatusCode(t, result, http.StatusInternalServerError)
}

type errorDatabase struct{}

func (errorDatabase) GetAlbums() ([]Album, error) {
	return nil, errors.New("GetAlbums error")
}

func (errorDatabase) GetAlbumByID(id string) (Album, error) {
	return Album{}, errors.New("GetAlbumByID error")
}

func (errorDatabase) AddAlbum(album Album) error {
	return errors.New("AddAlbum error")
}

func newTestServer() *Server {
	db := NewMemoryDatabase()
	db.AddAlbum(Album{ID: "a2", Title: "Hey Jude", Artist: "The Beetles", Price: 2000})
	db.AddAlbum(Album{ID: "a1", Title: "9th Symphony", Artist: "Beethoven", Price: 795})
	server := NewServer(db, log.New(io.Discard, "", 0))
	return server
}

func serve(t *testing.T, server *Server, request *http.Request) *http.Response {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder.Result()
}

func newRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("error creating request: %v", err)
	}
	return request
}

func unmarshalResponse(t *testing.T, body io.Reader, v interface{}) {
	t.Helper()
	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("error reading response body: %v", err)
	}
	err = json.Unmarshal(b, v)
	if err != nil {
		t.Fatalf("error unmarshaling JSON: %v", err)
	}
}

func ensureStatusCode(t *testing.T, response *http.Response, want int) {
	t.Helper()
	if response.StatusCode != want {
		t.Fatalf("bad status code: got %d, want %d", response.StatusCode, want)
	}
}
