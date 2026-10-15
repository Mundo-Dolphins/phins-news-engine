package engine

import "testing"

func TestOptionsServiceName_DefaultIsNeutral(t *testing.T) {
	if DefaultServiceName != "phins-news-engine" {
		t.Fatalf("DefaultServiceName = %q, want phins-news-engine", DefaultServiceName)
	}
	for _, name := range []string{"", "   "} {
		if got := (Options{ServiceName: name}).serviceName(); got != DefaultServiceName {
			t.Fatalf("serviceName() with %q = %q, want %q", name, got, DefaultServiceName)
		}
	}
}

func TestOptionsServiceName_Override(t *testing.T) {
	if got := (Options{ServiceName: "host-news-bot"}).serviceName(); got != "host-news-bot" {
		t.Fatalf("serviceName() = %q, want host-news-bot", got)
	}
}
