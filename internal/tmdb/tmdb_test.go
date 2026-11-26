package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuth(t *testing.T) {
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth = r.URL.Query().Get("api_key"), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	for _, tc := range []struct{ token, key, auth string }{
		{"0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef", ""},
		{"eyJh.eyJz.sig", "", "Bearer eyJh.eyJz.sig"},
	} {
		c := &Client{Token: tc.token, API: srv.URL, HTTP: srv.Client()}
		if _, err := c.Search(context.Background(), "movie", "x", ""); err != nil {
			t.Fatal(err)
		}
		if gotKey != tc.key || gotAuth != tc.auth {
			t.Errorf("token %q: api_key %q, Authorization %q", tc.token, gotKey, gotAuth)
		}
	}
}

func TestErrorHidesKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	key := "0123456789abcdef0123456789abcdef"
	c := &Client{Token: key, API: srv.URL, HTTP: http.DefaultClient}
	_, err := c.Search(context.Background(), "movie", "x", "")
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("error %v", err)
	}
}
