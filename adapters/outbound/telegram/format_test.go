package telegram

import (
	"errors"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

func TestBuildMessage_EscapesTitleAndSummary(t *testing.T) {
	a := article.Article{
		Title: "Isotopes & Sharks <preview>", Summary: "A vs B > C",
		CanonicalURL: "https://example.com/story", Source: source.Source{Name: "Example"},
	}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if strings.Contains(msg, "<preview>") || strings.Contains(msg, " > C") {
		t.Errorf("msg = %q, want < and > escaped", msg)
	}
	if !strings.Contains(msg, "Isotopes &amp; Sharks &lt;preview&gt;") {
		t.Errorf("msg = %q, want escaped title", msg)
	}
}

func TestBuildMessage_EscapesURLInHrefAttribute(t *testing.T) {
	a := article.Article{Title: "T", CanonicalURL: `https://example.com/a"b&c`}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if strings.Contains(msg, `"b&c"`) {
		t.Errorf("msg = %q, want the URL's own quote/ampersand escaped inside href", msg)
	}
	if !strings.Contains(msg, `href="https://example.com/a&quot;b&amp;c"`) {
		t.Errorf("msg = %q, want escaped href", msg)
	}
}

func TestBuildMessage_IncludesSourceNameAndLink(t *testing.T) {
	a := article.Article{Title: "T", CanonicalURL: "https://example.com/a", Source: source.Source{Name: "Springfield Herald"}}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if !strings.Contains(msg, `<a href="https://example.com/a">Leer más</a> — Springfield Herald`) {
		t.Errorf("msg = %q, want a link followed by the source name", msg)
	}
}

func TestBuildMessage_CleansNewswireTitleNoise(t *testing.T) {
	a := article.Article{
		Title:        "NEW: WATCH: Isotopes &amp; Sharks preview https://t.co/abc123",
		CanonicalURL: "https://example.com/story",
		Source:       source.Source{Name: "Example"},
	}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if strings.Contains(msg, "NEW:") || strings.Contains(msg, "WATCH:") || strings.Contains(msg, "https://t.co/abc123") {
		t.Errorf("msg = %q, want title prefixes and inline URL removed", msg)
	}
	if !strings.Contains(msg, "<b>Isotopes &amp; Sharks preview</b>") {
		t.Errorf("msg = %q, want cleaned and escaped title", msg)
	}
}

func TestBuildMessage_CleansSummaryInlineURLAndEntities(t *testing.T) {
	a := article.Article{
		Title:        "T",
		Summary:      "Mike Thistlewood &amp; Ezo discuss camp. https://example.com/duplicated",
		CanonicalURL: "https://example.com/story",
		Source:       source.Source{Name: "Example"},
	}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if strings.Contains(msg, "https://example.com/duplicated") {
		t.Errorf("msg = %q, want inline summary URL removed", msg)
	}
	if !strings.Contains(msg, "Mike Thistlewood &amp; Ezo discuss camp.") {
		t.Errorf("msg = %q, want summary entities decoded before escaping once", msg)
	}
}

func TestBuildMessage_UsesFallbackTitleWhenCleanupEmptiesTitle(t *testing.T) {
	a := article.Article{Title: "NEW: https://t.co/abc123", CanonicalURL: "https://example.com/story"}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if !strings.Contains(msg, "<b>Sin titulo</b>") {
		t.Errorf("msg = %q, want fallback title", msg)
	}
}

func TestBuildMessage_PrefixesSubstantialUpdateWithUpdateMarker(t *testing.T) {
	a := article.Article{Title: "T", CanonicalURL: "https://example.com/a", ClusterMemberRole: string(clustering.MemberRoleSubstantialUpdate)}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if !strings.HasPrefix(msg, "🔄 <b>Actualización</b>\n") {
		t.Errorf("msg = %q, want an update-marker prefix", msg)
	}
}

func TestBuildMessage_PrefixesCorrectionWithUpdateMarker(t *testing.T) {
	a := article.Article{Title: "T", CanonicalURL: "https://example.com/a", ClusterMemberRole: string(clustering.MemberRoleCorrection)}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if !strings.HasPrefix(msg, "🔄 <b>Actualización</b>\n") {
		t.Errorf("msg = %q, want an update-marker prefix", msg)
	}
}

func TestBuildMessage_FirstReportHasNoUpdateMarker(t *testing.T) {
	a := article.Article{Title: "T", CanonicalURL: "https://example.com/a", ClusterMemberRole: string(clustering.MemberRoleFirstReport)}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if strings.Contains(msg, "Actualización") {
		t.Errorf("msg = %q, want no update marker for a first report", msg)
	}
}

func TestBuildMessage_TruncatesLongSummaryToFitMaxMessageUTF16Units(t *testing.T) {
	a := article.Article{
		Title: "T", CanonicalURL: "https://example.com/a",
		Summary: strings.Repeat("word ", 2000),
	}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if n := utf16Len(msg); n > maxMessageUTF16Units {
		t.Errorf("utf16Len(msg) = %d, want <= %d", n, maxMessageUTF16Units)
	}
	if !strings.Contains(msg, "…") {
		t.Errorf("msg does not contain a truncation ellipsis despite an oversized summary")
	}
}

func TestBuildMessage_EmptySummaryOmitsBlankParagraph(t *testing.T) {
	a := article.Article{Title: "T", CanonicalURL: "https://example.com/a", Summary: ""}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if strings.Count(msg, "\n\n") != 1 {
		t.Errorf("msg = %q, want exactly one blank-line separator (title -> link) when summary is empty", msg)
	}
}

// TestBuildMessage_TruncatesByUTF16UnitsNotRunes is a characterization test
// for a summary made entirely of astral emoji (2 UTF-16 units
// each, 1 Go rune each) must be truncated far short of what a rune-based
// budget would have allowed, because Telegram counts UTF-16 units.
func TestBuildMessage_TruncatesByUTF16UnitsNotRunes(t *testing.T) {
	a := article.Article{
		Title: "T", CanonicalURL: "https://example.com/a",
		Summary: strings.Repeat("🔥", 3000), // 3000 runes, 6000 UTF-16 units
	}
	msg, err := buildMessage(a)
	if err != nil {
		t.Fatalf("buildMessage returned an unexpected error: %v", err)
	}
	if n := utf16Len(msg); n > maxMessageUTF16Units {
		t.Errorf("utf16Len(msg) = %d, want <= %d", n, maxMessageUTF16Units)
	}
	// The old rune-based budget (also 4096) would not have truncated at
	// all: 3000 runes < 4096. Any truncation below the full 3000 proves
	// the budget is being charged in UTF-16 units (2 per emoji), not runes.
	if got := strings.Count(msg, "🔥"); got >= 3000 {
		t.Errorf("msg contains all %d fire emoji untruncated, want fewer (proves UTF-16, not rune, budgeting)", got)
	}
}

// TestBuildMessage_OversizedTitleReturnsError: title and link are never
// truncated (only the summary's budget shrinks); if the fixed parts alone
// still exceed Telegram's limit even with an empty summary, buildMessage
// must fail locally rather than hand the publisher an oversized payload.
func TestBuildMessage_OversizedTitleReturnsError(t *testing.T) {
	a := article.Article{
		Title:        strings.Repeat("word ", 1000), // way over 4096 UTF-16 units once wrapped in <b>...</b>
		CanonicalURL: "https://example.com/a",
	}
	msg, err := buildMessage(a)
	if err == nil {
		t.Fatalf("buildMessage returned no error for an oversized title; msg = %q", msg)
	}
	if !errors.Is(err, errMessageTooLong) {
		t.Errorf("buildMessage error = %v, want errors.Is(err, errMessageTooLong)", err)
	}
	if msg != "" {
		t.Errorf("buildMessage returned msg = %q alongside an error, want \"\"", msg)
	}
}
