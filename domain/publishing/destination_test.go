package publishing

import "testing"

func TestDestinationZeroValueIsTelegram(t *testing.T) {
	if got := Destination("").Normalized(); got != DestinationTelegram {
		t.Fatalf("got %q", got)
	}
}

func TestValidateDestination(t *testing.T) {
	for _, d := range []Destination{"", DestinationTelegram, DestinationBluesky} {
		if err := ValidateDestination(d); err != nil {
			t.Fatalf("%q: %v", d, err)
		}
	}
	if err := ValidateDestination("mastodon"); err == nil {
		t.Fatal("unknown destination accepted")
	}
}
