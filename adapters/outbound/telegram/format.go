package telegram

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
)

// maxMessageUTF16Units is Telegram's sendMessage text length cap, measured
// in UTF-16 code units — the unit Telegram's Bot API itself uses for this
// limit and for MessageEntity offset/length (see
// https://core.telegram.org/api/entities#entity-length). It is not Go
// runes: codepoints outside the Basic Multilingual Plane (many emoji,
// among others) count as 1 rune but 2 UTF-16 units.
const maxMessageUTF16Units = 4096

// errMessageTooLong is wrapped into the error buildMessage returns when
// the assembled message still exceeds maxMessageUTF16Units even after
// truncating the summary as far as truncateSummaryHTML allows — i.e. the
// fixed, never-truncated parts (prefix + title + link) alone are too long.
var errMessageTooLong = errors.New("telegram: message exceeds Telegram's UTF-16 length limit even after truncation")

var (
	inlineURLPattern       = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>()]+`)
	editorialPrefixPattern = regexp.MustCompile(`(?i)^\s*(?:new|watch|video|photos|analysis|report|rumor|rumors|breaking|update)\s*:\s*`)
)

// escapeHTML escapes the three characters Telegram's HTML parse_mode
// requires escaped in text nodes: & first (so the entities it introduces
// are never themselves re-escaped), then < and >.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// escapeHTMLAttribute escapes s for use inside an HTML attribute value
// (e.g. href="..."): everything escapeHTML does, plus the double quote
// that would otherwise close the attribute early.
func escapeHTMLAttribute(s string) string {
	return strings.ReplaceAll(escapeHTML(s), `"`, "&quot;")
}

func cleanTelegramText(s string, stripPrefixes bool) string {
	s = html.UnescapeString(strings.TrimSpace(s))
	s = inlineURLPattern.ReplaceAllString(s, "")
	if stripPrefixes {
		for {
			cleaned := editorialPrefixPattern.ReplaceAllString(s, "")
			if cleaned == s {
				break
			}
			s = cleaned
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

// isUpdate reports whether role marks a as a development on an
// already-reported story (a genuine update or a correction), rather than a
// first report.
func isUpdate(role string) bool {
	return role == string(clustering.MemberRoleSubstantialUpdate) || role == string(clustering.MemberRoleCorrection)
}

// buildMessage renders a as an HTML-formatted (parse_mode=HTML) Telegram
// message: an "🔄 Actualización" marker when a.ClusterMemberRole indicates a
// substantial update or correction (see isUpdate), a bold escaped title, a
// summary truncated to fit Telegram's length cap, and a link back to the
// source with the source's name. disable_web_page_preview is left to the
// caller (publisher.go) — Telegram generates its own preview from the
// link.
//
// Truncation policy: prefix, title, and link are never truncated — only
// the summary's budget shrinks to make room for them. If the fixed parts
// alone still leave no room for a valid payload (e.g. an unusually long
// title), buildMessage returns an error wrapping errMessageTooLong instead
// of a string, rather than handing the publisher a payload Telegram will
// reject.
func buildMessage(a article.Article) (string, error) {
	var prefix string
	if isUpdate(a.ClusterMemberRole) {
		prefix = "🔄 <b>Actualización</b>\n"
	}

	displayTitle := cleanTelegramText(a.Title, true)
	if displayTitle == "" {
		displayTitle = "Sin titulo"
	}
	title := "<b>" + escapeHTML(displayTitle) + "</b>"

	link := fmt.Sprintf(`<a href="%s">Leer más</a>`, escapeHTMLAttribute(a.CanonicalURL))
	if a.Source.Name != "" {
		link += " — " + escapeHTML(a.Source.Name)
	}

	fixedLen := utf16Len(prefix) + utf16Len(title) + utf16Len(link) + utf16Len("\n\n\n\n")
	summary := truncateSummaryHTML(cleanTelegramText(a.Summary, false), maxMessageUTF16Units-fixedLen)

	msg := prefix + title
	if summary != "" {
		msg += "\n\n" + summary
	}
	msg += "\n\n" + link

	if n := utf16Len(msg); n > maxMessageUTF16Units {
		return "", fmt.Errorf("%w: %d UTF-16 units (limit %d) for article %s", errMessageTooLong, n, maxMessageUTF16Units, a.ID)
	}
	return msg, nil
}
