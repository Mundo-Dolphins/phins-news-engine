package normalization

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// StripHTML parses s as HTML and returns only its visible text, with
// whitespace collapsed. Tag names and attribute values (href, target,
// rel, src, ...) never appear in the output. If s isn't valid HTML,
// goquery's lenient parser still returns readable text rather than
// erroring.
func StripHTML(s string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(s))
	if err != nil {
		return collapseSpace(s)
	}
	return collapseSpace(doc.Text())
}
