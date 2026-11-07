package publishing

import (
	"reflect"
	"testing"
)

func TestParseMessagePayload_LegacyRowsAreEmpty(t *testing.T) {
	for _, raw := range []string{"", "{}", `{"title":"T","canonical_url":"https://x.test/a"}`} {
		p, err := ParseMessagePayload(raw)
		if err != nil {
			t.Fatalf("ParseMessagePayload(%q) error = %v", raw, err)
		}
		if !p.IsEmpty() {
			t.Errorf("ParseMessagePayload(%q) = %+v, want empty", raw, p)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", raw, err)
		}
	}
}

func TestMessagePayload_RoundTrip(t *testing.T) {
	want := MessagePayload{
		Text:   "Primer injury report de la semana 8",
		Images: []ImageRef{{Path: "reports/w8.png", MIME: "image/png", Alt: "Injury report"}},
		Link:   "https://example.test/report",
		Kind:   "injury_report",
	}
	raw, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMessagePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if empty, _ := (MessagePayload{}).Marshal(); empty != "{}" {
		t.Errorf("empty payload marshals to %q, want {}", empty)
	}
}

func TestParseMessagePayload_RejectsMalformedJSON(t *testing.T) {
	if _, err := ParseMessagePayload("not json"); err == nil {
		t.Fatal("want error for malformed JSON")
	}
}

func TestMessagePayload_Validate(t *testing.T) {
	img := func(path, mime string) []ImageRef { return []ImageRef{{Path: path, MIME: mime}} }
	tests := []struct {
		name    string
		p       MessagePayload
		wantErr bool
	}{
		{"text only", MessagePayload{Text: "hi"}, false},
		{"text and image", MessagePayload{Text: "hi", Images: img("a/b.jpg", "image/jpeg")}, false},
		{"image only", MessagePayload{Images: img("b.jpg", "image/jpeg")}, false},
		{"empty path", MessagePayload{Text: "hi", Images: img("", "image/jpeg")}, true},
		{"absolute path", MessagePayload{Text: "hi", Images: img("/store/a.jpg", "image/jpeg")}, true},
		{"parent escape", MessagePayload{Text: "hi", Images: img("../x.jpg", "image/jpeg")}, true},
		{"nested escape", MessagePayload{Text: "hi", Images: img("a/../../x.jpg", "image/jpeg")}, true},
		{"windows drive", MessagePayload{Text: "hi", Images: img(`C:\x.jpg`, "image/jpeg")}, true},
		{"non image mime", MessagePayload{Text: "hi", Images: img("a.jpg", "text/html")}, true},
		{"relative link", MessagePayload{Text: "hi", Link: "/x"}, true},
		{"file link", MessagePayload{Text: "hi", Link: "file:///x"}, true},
		{"link without content", MessagePayload{Link: "https://x.test"}, true},
		{"kind without content", MessagePayload{Kind: "k"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.p.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidate_Continuations(t *testing.T) {
	img := []ImageRef{{Path: "a.jpg", MIME: "image/jpeg"}}
	tests := []struct {
		name    string
		p       MessagePayload
		wantErr bool
	}{
		{"empty continuation", MessagePayload{Text: "a", Continuations: []string{""}}, true},
		{"blank continuation", MessagePayload{Text: "a", Continuations: []string{"b", "  \n"}}, true},
		{"with images", MessagePayload{Text: "a", Images: img, Continuations: []string{"b"}}, true},
		{"without text", MessagePayload{Continuations: []string{"b"}}, true},
		{"two continuations", MessagePayload{Text: "a", Continuations: []string{"b", "c"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.p.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParts(t *testing.T) {
	got := MessagePayload{Text: "a", Continuations: []string{"b", "c"}}.Parts()
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Parts() = %v, want %v", got, want)
	}
}

func TestThreadProgressRoundTrip(t *testing.T) {
	for _, raw := range []string{"", "[]"} {
		got, err := ParseThreadProgress(raw)
		if err != nil || len(got) != 0 {
			t.Fatalf("ParseThreadProgress(%q) = %v, %v; want empty", raw, got, err)
		}
	}
	if s, err := MarshalThreadProgress(nil); err != nil || s != "[]" {
		t.Fatalf("MarshalThreadProgress(nil) = %q, %v; want []", s, err)
	}
	refs := []PostRef{{URI: "at://d/p/1", CID: "c1"}, {URI: "at://d/p/2", CID: "c2"}}
	raw, err := MarshalThreadProgress(refs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseThreadProgress(raw)
	if err != nil || !reflect.DeepEqual(got, refs) {
		t.Fatalf("round trip = %v, %v; want %v", got, err, refs)
	}
}

func TestPostRefWebURL(t *testing.T) {
	cases := map[string]string{
		"at://did:plc:abc123/app.bsky.feed.post/3kxyz": "https://bsky.app/profile/did:plc:abc123/post/3kxyz",
		"at://did:plc:abc123/app.bsky.feed.like/3kxyz": "",
		"at://t/p/0": "",
		"":           "",
	}
	for uri, want := range cases {
		if got := (PostRef{URI: uri}).WebURL(); got != want {
			t.Errorf("WebURL(%q) = %q, want %q", uri, got, want)
		}
	}
}
