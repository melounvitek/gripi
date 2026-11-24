package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
)

func TestScratchPromptImagesStayForTheirOwnerUntilNewerOnesTakeTheirPlace(t *testing.T) {
	app := &application{scratch: scratchSessions{byOwner: map[string]*scratchSession{"alice": {key: "a"}, "bob": {key: "b"}}, ended: map[string]bool{}}}
	first, err := app.scratch.keepImage("a", bytes.Repeat([]byte{1}, 40<<20), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.scratch.keepImage("a", bytes.Repeat([]byte{2}, 30<<20), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	get := func(source, owner string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, source, nil)
		request.SetPathValue("token", path.Base(source))
		request.Header.Set("Cookie", workspaceCookieName+"="+owner)
		response := httptest.NewRecorder()
		app.scratchPromptImage(response, request)
		return response
	}

	if response := get(first, "alice"); response.Code != http.StatusNotFound {
		t.Fatalf("an image that newer ones replaced = %d", response.Code)
	}
	if response := get(second, "alice"); response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/jpeg" || response.Body.Len() != 30<<20 {
		t.Fatalf("the latest image = %d %s, %d bytes", response.Code, response.Header().Get("Content-Type"), response.Body.Len())
	}
	if response := get(second, "bob"); response.Code != http.StatusNotFound {
		t.Fatalf("another user's image = %d", response.Code)
	}
	app.scratch.ended["a"] = true
	if response := get(second, "alice"); response.Code != http.StatusNotFound {
		t.Fatalf("an ended scratch session's image = %d", response.Code)
	}
}
