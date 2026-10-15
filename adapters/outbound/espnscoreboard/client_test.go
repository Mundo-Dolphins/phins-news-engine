package espnscoreboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
)

const scoreboardFixture = `{"events":[
	{"id":"401671000","status":{"period":3,"displayClock":"09:12","type":{"name":"STATUS_IN_PROGRESS","state":"in","completed":false}},
	 "competitions":[{"competitors":[
	   {"homeAway":"home","team":{"abbreviation":"SPR","displayName":"Springfield Isotopes"}},
	   {"homeAway":"away","team":{"abbreviation":"CAP","displayName":"Capital City Capitals"}}
	 ]}]},
	{"id":"other-game","status":{"period":2,"displayClock":"05:00","type":{"name":"STATUS_IN_PROGRESS","state":"in","completed":false}},
	 "competitions":[{"competitors":[
	   {"homeAway":"home","team":{"abbreviation":"SHE","displayName":"Shelbyville Sharks"}},
	   {"homeAway":"away","team":{"abbreviation":"OGD","displayName":"Ogdenville Owls"}}
	 ]}]}
]}`

func TestClient_Current_LiveGameFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(scoreboardFixture))
	}))
	defer srv.Close()

	c := NewClient("spr", "", time.Second)
	c.Host = srv.URL

	state, ok, err := c.Current(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if state.Phase != matchstate.Q3 || state.MatchID != "401671000" {
		t.Fatalf("state = %+v, want Q3/401671000", state)
	}
}

func TestClient_Current_NoGameTodayIsOkFalseNoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer srv.Close()

	c := NewClient("spr", "", time.Second)
	c.Host = srv.URL

	_, ok, err := c.Current(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Current() error = %v, want nil (no game is a normal outcome, not a failure)", err)
	}
	if ok {
		t.Fatal("ok = true with zero events, want false")
	}
}

func TestClient_Current_HTTPFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient("spr", "", time.Second)
	c.Host = srv.URL

	_, ok, err := c.Current(context.Background(), time.Now())
	if err == nil {
		t.Fatal("err = nil, want an error for a 500 response")
	}
	if ok {
		t.Fatal("ok = true on HTTP failure, want false")
	}
}

func TestClient_Current_MalformedJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	c := NewClient("spr", "", time.Second)
	c.Host = srv.URL

	_, ok, err := c.Current(context.Background(), time.Now())
	if err == nil {
		t.Fatal("err = nil, want a decode error")
	}
	if ok {
		t.Fatal("ok = true on malformed JSON, want false")
	}
}

func TestClient_UserAgent_DefaultIsNeutral(t *testing.T) {
	if DefaultUserAgent != "phins-news-engine/1.0" {
		t.Fatalf("DefaultUserAgent = %q, want phins-news-engine/1.0", DefaultUserAgent)
	}
	gotUA := requestUserAgent(t, NewClient("spr", "", time.Second))
	if gotUA != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, DefaultUserAgent)
	}
}

func TestClient_UserAgent_Override(t *testing.T) {
	gotUA := requestUserAgent(t, NewClient("spr", "host-bot/2.0", time.Second))
	if gotUA != "host-bot/2.0" {
		t.Fatalf("User-Agent = %q, want host-bot/2.0", gotUA)
	}
}

func requestUserAgent(t *testing.T, c *Client) string {
	t.Helper()
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer srv.Close()
	c.Host = srv.URL
	if _, _, err := c.Current(context.Background(), time.Now()); err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	return gotUA
}
