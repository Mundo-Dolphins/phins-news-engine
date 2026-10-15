package source_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

func TestType_Valid(t *testing.T) {
	tests := []struct {
		name string
		typ  source.Type
		want bool
	}{
		{"rss is valid", source.TypeRSS, true},
		{"web is valid", source.TypeWeb, true},
		{"twitter is valid", source.TypeTwitter, true},
		{"bluesky is valid", source.TypeBluesky, true},
		{"instagram is valid", source.TypeInstagram, true},
		{"reddit is valid", source.TypeReddit, true},
		{"youtube is valid", source.TypeYouTube, true},
		{"other is valid", source.TypeOther, true},
		{"unknown is invalid", source.Type("carrier-pigeon"), false},
		{"empty is invalid", source.Type(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.typ.Valid(); got != tt.want {
				t.Errorf("Type(%q).Valid() = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestSource_Validate(t *testing.T) {
	tests := []struct {
		name    string
		src     source.Source
		wantErr bool
	}{
		{
			name: "valid source",
			src: source.Source{
				ID:   mustID(t, source.TypeRSS, "isotopes-rss"),
				Name: "Springfield Isotopes RSS",
				Type: source.TypeRSS,
			},
			wantErr: false,
		},
		{
			name:    "missing id",
			src:     source.Source{Name: "x", Type: source.TypeRSS},
			wantErr: true,
		},
		{
			name:    "missing name",
			src:     source.Source{ID: mustID(t, source.TypeRSS, "x"), Type: source.TypeRSS},
			wantErr: true,
		},
		{
			name:    "invalid type",
			src:     source.Source{ID: mustID(t, source.TypeRSS, "x"), Name: "x", Type: source.Type("bogus")},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.src.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func mustID(t *testing.T, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}
