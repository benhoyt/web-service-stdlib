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
	"testing/iotest"
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
	ensureStatus(t, result, http.StatusOK)

	var got []testAlbum
	unmarshalResponse(t, result, &got)
	want := []testAlbum{
		{ID: "a1", Title: "9th Symphony", Artist: "Beethoven", Price: 795},
		{ID: "a2", Title: "Hey Jude", Artist: "The Beatles", Price: 2000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bad response: got vs want:\n%#v\n%#v", got, want)
	}
}

func TestGetAlbum(t *testing.T) {
	server := newTestServer()

	tests := []getAlbumTest{
		{"/albums/", http.StatusNotFound, testAlbum{}},
		{"/albums/a1", http.StatusOK, testAlbum{ID: "a1", Title: "9th Symphony", Artist: "Beethoven", Price: 795}},
		{"/albums/a2", http.StatusOK, testAlbum{ID: "a2", Title: "Hey Jude", Artist: "The Beatles", Price: 2000}},
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
	path string
	code int
	want testAlbum
}

// This test logic is factored out as it's used in a few places.
func testGetAlbum(t *testing.T, server *Server, test getAlbumTest) {
	result := serve(t, server, newRequest(t, "GET", test.path, nil))
	ensureStatus(t, result, test.code)
	if test.code == http.StatusNotFound {
		ensureError(t, result, http.StatusNotFound, "not-found", nil)
		return
	}

	var got testAlbum
	unmarshalResponse(t, result, &got)
	if !reflect.DeepEqual(got, test.want) {
		t.Fatalf("bad response: got vs want:\n%#v\n%#v", got, test.want)
	}
}

func TestAddAlbumCreated(t *testing.T) {
	server := newTestServer()
	body := `{"id": "a9", "title": "Pianoman", "artist": "Billy Joel", "price": 1234}`
	result := serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(body)))
	ensureStatus(t, result, http.StatusCreated)

	var got testAlbum
	unmarshalResponse(t, result, &got)
	want := testAlbum{ID: "a9", Title: "Pianoman", Artist: "Billy Joel", Price: 1234}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bad response: got vs want:\n%#v\n%#v", got, want)
	}

	// Ensure we can fetch the album after it's been created
	testGetAlbum(t, server, getAlbumTest{"/albums/a9", http.StatusOK, want})

	// Ensure /albums lists the new album
	result = serve(t, server, newRequest(t, "GET", "/albums", nil))
	ensureStatus(t, result, http.StatusOK)
	var albums []testAlbum
	unmarshalResponse(t, result, &albums)
	for _, album := range albums {
		if album.ID == "a9" {
			if !reflect.DeepEqual(album, want) {
				t.Fatalf("bad response: got vs want:\n%#v\n%#v", album, want)
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
	ensureStatus(t, result, http.StatusConflict)
	ensureError(t, result, http.StatusConflict, "already-exists", nil)

	// Ensure it didn't modify the album
	want := testAlbum{ID: "a2", Title: "Hey Jude", Artist: "The Beatles", Price: 2000}
	testGetAlbum(t, server, getAlbumTest{"/albums/a2", http.StatusOK, want})
}

func TestAddAlbumBadJSON(t *testing.T) {
	server := newTestServer()
	result := serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader("@")))
	ensureStatus(t, result, http.StatusBadRequest)
	data := map[string]interface{}{
		"message": "invalid character '@' looking for beginning of value",
	}
	ensureError(t, result, http.StatusBadRequest, "malformed-json", data)
}

func TestAddAlbumMissingFields(t *testing.T) {
	server := newTestServer()
	result := serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(`{"price": -1}`)))
	ensureStatus(t, result, http.StatusBadRequest)
	data := map[string]interface{}{
		"id":     map[string]interface{}{"error": "required"},
		"title":  map[string]interface{}{"error": "required"},
		"artist": map[string]interface{}{"error": "required"},
		"price":  map[string]interface{}{"error": "out-of-range", "message": "price must be between 0 and $1000"},
	}
	ensureError(t, result, http.StatusBadRequest, "validation", data)
}

func TestConcurrentRequests(t *testing.T) {
	server := newTestServer()
	for i := 0; i < 100; i++ {
		go func(i int) {
			result := serve(t, server, newRequest(t, "GET", "/albums", nil))
			ensureStatus(t, result, http.StatusOK)

			albumID := "c" + strconv.Itoa(i)
			body := `{"id": "` + albumID + `", "title": "T", "artist": "A"}`
			result = serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(body)))
			ensureStatus(t, result, http.StatusCreated)

			result = serve(t, server, newRequest(t, "GET", "/albums/"+albumID, nil))
			ensureStatus(t, result, http.StatusOK)
		}(i)
	}
}

func TestDatabaseErrors(t *testing.T) {
	db := errorDatabase{}
	server := NewServer(db, log.New(io.Discard, "", 0))

	result := serve(t, server, newRequest(t, "GET", "/albums", nil))
	ensureStatus(t, result, http.StatusInternalServerError)
	ensureError(t, result, http.StatusInternalServerError, "database", nil)

	body := `{"id": "a9", "title": "Pianoman", "artist": "Billy Joel"}`
	result = serve(t, server, newRequest(t, "POST", "/albums", strings.NewReader(body)))
	ensureStatus(t, result, http.StatusInternalServerError)
	ensureError(t, result, http.StatusInternalServerError, "database", nil)

	result = serve(t, server, newRequest(t, "GET", "/albums/a1", nil))
	ensureStatus(t, result, http.StatusInternalServerError)
	ensureError(t, result, http.StatusInternalServerError, "database", nil)
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

func TestMethodNotAllowed(t *testing.T) {
	server := newTestServer()
	result := serve(t, server, newRequest(t, "PUT", "/albums", nil))
	ensureStatus(t, result, http.StatusMethodNotAllowed)
	ensureError(t, result, http.StatusMethodNotAllowed, "method-not-allowed", nil)
	allow := result.Header.Get("Allow")
	if allow != "GET, POST" {
		t.Fatalf("bad Allow header: got %q, want %q", allow, "GET, POST")
	}

	result = serve(t, server, newRequest(t, "PUT", "/albums/a1", nil))
	ensureStatus(t, result, http.StatusMethodNotAllowed)
	ensureError(t, result, http.StatusMethodNotAllowed, "method-not-allowed", nil)
	allow = result.Header.Get("Allow")
	if allow != "GET" {
		t.Fatalf("bad Allow header: got %q, want %q", allow, "GET")
	}
}

func TestReadJSONReadError(t *testing.T) {
	server := newTestServer()
	errReader := iotest.ErrReader(errors.New("error"))
	result := serve(t, server, newRequest(t, "POST", "/albums", errReader))
	ensureStatus(t, result, http.StatusInternalServerError)
	ensureError(t, result, http.StatusInternalServerError, "internal", nil)
}

func newTestServer() *Server {
	db := NewMemoryDatabase()
	db.AddAlbum(Album{ID: "a2", Title: "Hey Jude", Artist: "The Beatles", Price: 2000})
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

func unmarshalResponse(t *testing.T, response *http.Response, v interface{}) {
	t.Helper()
	got := response.Header.Get("Content-Type")
	want := "application/json; charset=utf-8"
	if got != want {
		t.Fatalf("bad Content-Type header: got %q, want %q", got, want)
	}
	b, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("error reading response body: %v", err)
	}
	err = json.Unmarshal(b, v)
	if err != nil {
		t.Fatalf("error unmarshaling JSON: %v", err)
	}
}

func ensureStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	if response.StatusCode != want {
		t.Fatalf("bad status code: got %d, want %d", response.StatusCode, want)
	}
}

func ensureError(t *testing.T, response *http.Response, status int, error string, data map[string]interface{}) {
	t.Helper()
	type errorResponse struct {
		Status int                    `json:"status"`
		Error  string                 `json:"error"`
		Data   map[string]interface{} `json:"data"`
	}
	var got errorResponse
	unmarshalResponse(t, response, &got)
	want := errorResponse{
		Status: status,
		Error:  error,
		Data:   data,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bad error: got vs want:\n%#v\n%#v", got, want)
	}
}
