package normalization_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

func TestStripHTML_RemovesTagsKeepingText(t *testing.T) {
	got := normalization.StripHTML("<p>Hello <b>world</b></p>")
	want := "Hello world"
	if got != want {
		t.Errorf("StripHTML() = %q, want %q", got, want)
	}
}

func TestStripHTML_DropsAttributeNamesAndValues(t *testing.T) {
	got := normalization.StripHTML(`<a href="https://example.com" target="_blank" rel="nofollow">Follow us</a>`)
	want := "Follow us"
	if got != want {
		t.Errorf("StripHTML() = %q, want %q (href/target/blank/nofollow must not leak into the text)", got, want)
	}
}

func TestStripHTML_CollapsesWhitespace(t *testing.T) {
	got := normalization.StripHTML("<div>\n  Line one  \n\n  <span>Line two</span>\n</div>")
	want := "Line one Line two"
	if got != want {
		t.Errorf("StripHTML() = %q, want %q", got, want)
	}
}

func TestStripHTML_PlainTextUnchangedApartFromSpacing(t *testing.T) {
	got := normalization.StripHTML("Isotopes sign new WR")
	want := "Isotopes sign new WR"
	if got != want {
		t.Errorf("StripHTML() = %q, want %q", got, want)
	}
}

func TestStripHTML_DecodesEntities(t *testing.T) {
	got := normalization.StripHTML("<p>Isotopes &amp; Sharks</p>")
	want := "Isotopes & Sharks"
	if got != want {
		t.Errorf("StripHTML() = %q, want %q", got, want)
	}
}

func TestStripHTMLBlocksKeepsBlockBoundariesAsLines(t *testing.T) {
	cases := map[string]string{
		"<p>Camp opens with   tryouts.</p><ul><li>A</li><li>B</li></ul>":   "Camp opens with tryouts.\nA\nB",
		"<p>One<br>Two<br/>Three</p>":                                      "One\nTwo\nThree",
		"<h2>Title</h2><div>Body <b>bold</b>\n text</div>":                 "Title\nBody bold text",
		"<table><tr><td>a</td> <td>b</td></tr><tr><td>c</td></tr></table>": "a b\nc",
		"<p>x</p><script>var y = 1;</script><style>p{}</style>":            "x",
		"plain   text": "plain text",
	}
	for in, want := range cases {
		if got := normalization.StripHTMLBlocks(in); got != want {
			t.Errorf("StripHTMLBlocks(%q) = %q, want %q", in, got, want)
		}
	}
}
