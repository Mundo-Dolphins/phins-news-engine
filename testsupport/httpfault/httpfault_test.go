package httpfault_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

func TestServersMisbehaveAsDocumented(t *testing.T) {
	client := &http.Client{Timeout: 100 * time.Millisecond}
	if resp, err := client.Get(httpfault.Hanging(t).URL); err == nil {
		resp.Body.Close()
		t.Fatal("Hanging answered")
	}
	if resp, err := client.Get(httpfault.Closing(t).URL); err == nil {
		resp.Body.Close()
		t.Fatal("Closing answered")
	}
	loop, hits := httpfault.RedirectLoop(t)
	if resp, err := client.Get(loop.URL); err == nil {
		resp.Body.Close()
		t.Fatal("RedirectLoop ended")
	}
	if hits.Load() == 0 {
		t.Fatal("RedirectLoop not hit")
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, httpfault.Oversized(t, 10).URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); len(body) != 10 {
		t.Fatalf("Oversized body = %d bytes", len(body))
	}
}
