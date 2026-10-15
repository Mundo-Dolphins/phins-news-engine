package knowledge_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

func ptr(t time.Time) *time.Time { return &t }

func TestStatement_ValidAt(t *testing.T) {
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name string
		st   knowledge.Statement
		at   time.Time
		want bool
	}{
		{"open both ends", knowledge.Statement{Rank: knowledge.RankNormal}, d(2024, 1, 1), true},
		{"before start", knowledge.Statement{Rank: knowledge.RankNormal, ValidFrom: ptr(d(2020, 1, 1))}, d(2019, 12, 31), false},
		{"at start inclusive", knowledge.Statement{Rank: knowledge.RankNormal, ValidFrom: ptr(d(2020, 1, 1))}, d(2020, 1, 1), true},
		{"at end exclusive", knowledge.Statement{Rank: knowledge.RankNormal, ValidTo: ptr(d(2022, 3, 1))}, d(2022, 3, 1), false},
		{"inside window", knowledge.Statement{Rank: knowledge.RankPreferred, ValidFrom: ptr(d(2020, 1, 1)), ValidTo: ptr(d(2022, 3, 1))}, d(2021, 6, 1), true},
		{"deprecated never valid", knowledge.Statement{Rank: knowledge.RankDeprecated}, d(2021, 6, 1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.st.ValidAt(tt.at); got != tt.want {
				t.Fatalf("ValidAt(%v) = %v, want %v", tt.at, got, tt.want)
			}
		})
	}
}
