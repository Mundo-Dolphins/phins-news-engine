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

// blockSelector lists the elements whose boundaries StripHTMLBlocks keeps as
// line breaks.
const blockSelector = "p, li, div, h1, h2, h3, h4, h5, h6, tr"

// lineBreak marks a kept line break while the text is extracted, so newlines
// in the source text itself still collapse like any other whitespace.
const lineBreak = "\x1e"

// StripHTMLBlocks is StripHTML that keeps the block structure: each block
// element (p, li, div, h1-h6, tr) and each <br> ends a line, whitespace
// (source newlines included) is collapsed within each line, and blank lines
// are dropped. Script and style contents are dropped. Use it where line
// structure matters (lists, paragraphs); StripHTML stays the single-line form.
func StripHTMLBlocks(s string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(s))
	if err != nil {
		return collapseSpace(s)
	}
	doc.Find("script, style").Remove()
	doc.Find("br").ReplaceWithHtml(lineBreak)
	doc.Find(blockSelector).Each(func(_ int, sel *goquery.Selection) {
		sel.BeforeHtml(lineBreak)
		sel.AfterHtml(lineBreak)
	})
	var lines []string
	for _, line := range strings.Split(doc.Text(), lineBreak) {
		if l := collapseSpace(line); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}
