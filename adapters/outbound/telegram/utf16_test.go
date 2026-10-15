package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUtf16Len_ASCII(t *testing.T) {
	if got := utf16Len("hello"); got != 5 {
		t.Errorf("utf16Len(%q) = %d, want 5", "hello", got)
	}
}

// TestUtf16Len_AstralCharacterCountsAsTwoUnits demonstrates the bug this
// package fixes: len([]rune("🔥")) is 1, but Telegram counts astral
// (non-BMP) codepoints as 2 UTF-16 code units (a surrogate pair).
func TestUtf16Len_AstralCharacterCountsAsTwoUnits(t *testing.T) {
	s := "🔥" // U+1F525, outside the Basic Multilingual Plane
	if got := len([]rune(s)); got != 1 {
		t.Fatalf("precondition failed: len([]rune(%q)) = %d, want 1", s, got)
	}
	if got := utf16Len(s); got != 2 {
		t.Errorf("utf16Len(%q) = %d, want 2", s, got)
	}
}

func TestTruncateSummaryHTML_FitsUnderBudgetReturnsEscapedUnchanged(t *testing.T) {
	got := truncateSummaryHTML("A vs B > C", 100)
	want := "A vs B &gt; C"
	if got != want {
		t.Errorf("truncateSummaryHTML = %q, want %q", got, want)
	}
}

// TestTruncateSummaryHTML_RespectsUTF16BudgetNotRuneBudget is a
// characterization test for the original bug: "🔥🔥🔥🔥🔥" is 5 runes but 10
// UTF-16 units. A rune-based truncator given budget 7 would keep all 5
// emoji (5 <= 7) and add no ellipsis; the UTF-16-aware truncator must cut
// it down because 10 > 7.
func TestTruncateSummaryHTML_RespectsUTF16BudgetNotRuneBudget(t *testing.T) {
	raw := strings.Repeat("🔥", 5)
	if got := len([]rune(raw)); got != 5 {
		t.Fatalf("precondition failed: len([]rune(raw)) = %d, want 5", got)
	}

	got := truncateSummaryHTML(raw, 7)

	if n := utf16Len(got); n > 7 {
		t.Errorf("utf16Len(truncateSummaryHTML(raw, 7)) = %d, want <= 7; got %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateSummaryHTML(raw, 7) = %q, want a truncation ellipsis", got)
	}
}

// TestTruncateSummaryHTML_NeverSplitsAnEscapeSequence guards against
// truncating post-escape (e.g. cutting "&amp;" into "&am"), which would
// leave invalid/incomplete HTML for Telegram's parse_mode=HTML to choke
// on. Every "&" in the output of a truncated, escaped string must start a
// complete "&amp;"/"&lt;"/"&gt;" entity.
func TestTruncateSummaryHTML_NeverSplitsAnEscapeSequence(t *testing.T) {
	for budget := 3; budget <= 12; budget++ {
		got := truncateSummaryHTML("A & B & C", budget)
		if bad, ok := findIncompleteEntity(got); ok {
			t.Errorf("truncateSummaryHTML(%q, %d) = %q, contains incomplete entity %q", "A & B & C", budget, got, bad)
		}
	}
}

// findIncompleteEntity reports the first "&"-prefixed substring in s that
// is not a complete &amp;/&lt;/&gt; entity, if any.
func findIncompleteEntity(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			continue
		}
		rest := s[i:]
		if strings.HasPrefix(rest, "&amp;") || strings.HasPrefix(rest, "&lt;") || strings.HasPrefix(rest, "&gt;") {
			continue
		}
		return rest, true
	}
	return "", false
}

// TestTruncateSummaryHTML_DoesNotSplitAZWJFamilyEmoji: "👨‍👩‍👧‍👦" (man, ZWJ,
// woman, ZWJ, girl, ZWJ, boy) is one grapheme cluster held together by
// ZWJs. Budget 12 makes the naive UTF-16-budget cut land right before the
// second ZWJ (after "Watch 👨‍👩") — a cut the fix must move backward past
// the whole joined run, not merely past the final dangling ZWJ.
func TestTruncateSummaryHTML_DoesNotSplitAZWJFamilyEmoji(t *testing.T) {
	family := "👨‍👩‍👧‍👦"
	raw := "Watch " + family + " tonight"
	rawRunes := []rune(raw)

	got := truncateSummaryHTML(raw, 12)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncateSummaryHTML(raw, 12) = %q, want it truncated", got)
	}
	body := []rune(strings.TrimSuffix(got, "…"))

	if k := len(body); k > 0 && isZWJ(body[k-1]) {
		t.Errorf("truncateSummaryHTML = %q, ends with a dangling ZWJ", got)
	}
	if k := len(body); k < len(rawRunes) && isZWJ(rawRunes[k]) {
		t.Errorf("truncateSummaryHTML = %q, cuts immediately before a ZWJ (splits the joined sequence)", got)
	}
}

// TestTruncateSummaryHTML_DoesNotSplitAFlagPair: a flag emoji is two
// regional-indicator codepoints (e.g. 🇪🇸 = U+1F1EA U+1F1F8). Cutting
// between them leaves one lone regional indicator instead of a flag.
func TestTruncateSummaryHTML_DoesNotSplitAFlagPair(t *testing.T) {
	flag := "\U0001F1EA\U0001F1F8" // 🇪🇸
	got := truncateSummaryHTML("Team "+flag+" wins", 9)

	body := strings.TrimSuffix(got, "…")
	runes := []rune(body)
	if len(runes) > 0 {
		n := 0
		for i := len(runes) - 1; i >= 0 && isRegionalIndicator(runes[i]); i-- {
			n++
		}
		if n%2 != 0 {
			t.Errorf("truncateSummaryHTML = %q, ends on half of a regional-indicator flag pair", got)
		}
	}
}

// TestTruncateSummaryHTML_DoesNotSplitASkinToneModifier: "👍🏽" is a base
// emoji followed by a Fitzpatrick skin-tone modifier. Budget 8 makes the
// naive UTF-16-budget cut land right before the modifier (keeping the bare
// "👍" and dropping "🏽") — the fix must drop the base too rather than
// silently changing the emoji's meaning.
func TestTruncateSummaryHTML_DoesNotSplitASkinToneModifier(t *testing.T) {
	thumb := "\U0001F44D\U0001F3FD" // 👍🏽
	raw := "Nice " + thumb + " work"
	rawRunes := []rune(raw)

	got := truncateSummaryHTML(raw, 8)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncateSummaryHTML(raw, 8) = %q, want it truncated", got)
	}
	body := []rune(strings.TrimSuffix(got, "…"))

	if k := len(body); k < len(rawRunes) && isSkinToneModifier(rawRunes[k]) {
		t.Errorf("truncateSummaryHTML = %q, cuts immediately before a skin-tone modifier (leaves the base emoji unmodified)", got)
	}
}

// TestTruncateSummaryHTML_TableDriven sweeps a broad set of scripts,
// emoji forms, and budgets, asserting the invariants every case must hold:
// valid UTF-8 out, budget <= 1 yields "", otherwise the UTF-16 length of
// the result never exceeds budget. Individual tests above cover the more
// specific "doesn't split X" behaviors; this table is the breadth sweep
// the issue asks for (limit-1/limit/limit+1, and cases where the rune
// count looks within budget but the UTF-16 count doesn't).
func TestTruncateSummaryHTML_TableDriven(t *testing.T) {
	longURL := "https://example.com/" + strings.Repeat("a", 5000)
	fraktur := strings.Repeat("𝔘", 10) // U+1D518: 10 runes, but 20 UTF-16 units (astral)

	cases := []struct {
		name   string
		raw    string
		budget int
	}{
		{"empty, zero budget", "", 0},
		{"empty, ample budget", "", 10},
		{"ascii, ample budget", "hello world", 100},
		{"ascii, exact limit", "hello world", 11},
		{"ascii, limit-1", "hello world", 10},
		{"ascii, limit+1", "hello world", 12},
		{"accented latin, ample budget", "café à la carte", 100},
		{"accented latin, truncated", "café à la carte", 6},
		{"decomposed combining mark, ample budget", "éclair", 100},
		{"decomposed combining mark, truncated at the mark", "éclair tonight", 2},
		{"cjk, ample budget", "こんにちは世界", 100},
		{"cjk, truncated", "こんにちは世界", 4},
		{"astral emoji run, limit-1", strings.Repeat("🔥", 5), 9},
		{"astral emoji run, limit exact", strings.Repeat("🔥", 5), 10},
		{"astral emoji run, limit+1", strings.Repeat("🔥", 5), 11},
		{"flag pair", "Team \U0001F1EA\U0001F1F8 wins", 9},
		{"skin tone modifier", "Nice \U0001F44D\U0001F3FD work", 8},
		{"zwj family", "Watch 👨‍👩‍👧‍👦 tonight", 12},
		{"newlines", "line one\nline two\nline three", 15},
		{"long url", longURL, 50},
		{"markup ampersand", "Isotopes & Sharks", 10},
		{"markup lt gt", "A < B > C", 6},
		{"rune count looks OK but UTF-16 doesn't", fraktur, 15},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateSummaryHTML(tc.raw, tc.budget)

			if !utf8.ValidString(got) {
				t.Fatalf("truncateSummaryHTML(%q, %d) = %q, not valid UTF-8", tc.raw, tc.budget, got)
			}
			if tc.budget <= 1 {
				if got != "" {
					t.Errorf("truncateSummaryHTML(%q, %d) = %q, want \"\" for budget <= 1", tc.raw, tc.budget, got)
				}
				return
			}
			if n := utf16Len(got); n > tc.budget {
				t.Errorf("truncateSummaryHTML(%q, %d) = %q, utf16Len = %d, want <= %d", tc.raw, tc.budget, got, n, tc.budget)
			}
		})
	}
}

// FuzzTruncateSummaryHTML asserts the truncator's core contract holds for
// arbitrary input: the result is always valid UTF-8, never exceeds the
// requested UTF-16 budget, and never panics.
func FuzzTruncateSummaryHTML(f *testing.F) {
	seeds := []string{
		"", "hello", "café", "こんにちは", "🔥🔥🔥🔥🔥",
		"Team \U0001F1EA\U0001F1F8 wins", "Nice \U0001F44D\U0001F3FD work",
		"Watch 👨‍👩‍👧‍👦 tonight", "A & B < C > D",
		"éclair", strings.Repeat("𝔘", 10),
	}
	for _, s := range seeds {
		for _, budget := range []int{0, 1, 2, 5, 10, 4096} {
			f.Add(s, budget)
		}
	}

	f.Fuzz(func(t *testing.T, raw string, budget int) {
		got := truncateSummaryHTML(raw, budget)

		if !utf8.ValidString(got) {
			t.Fatalf("truncateSummaryHTML(%q, %d) = %q, not valid UTF-8", raw, budget, got)
		}
		if budget <= 1 {
			if got != "" {
				t.Fatalf("truncateSummaryHTML(%q, %d) = %q, want \"\" for budget <= 1", raw, budget, got)
			}
			return
		}
		if n := utf16Len(got); n > budget {
			t.Fatalf("truncateSummaryHTML(%q, %d) = %q, utf16Len = %d, want <= %d", raw, budget, got, n, budget)
		}
	})
}

func BenchmarkTruncateSummaryHTML(b *testing.B) {
	raw := strings.Repeat("Isotopes beat the Sharks 24-17 in overtime & the crowd 🔥🔥🔥 went wild! ", 40)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		truncateSummaryHTML(raw, 300)
	}
}

func BenchmarkUtf16Len(b *testing.B) {
	raw := strings.Repeat("Isotopes beat the Sharks 24-17 in overtime & the crowd 🔥🔥🔥 went wild! ", 40)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		utf16Len(raw)
	}
}
