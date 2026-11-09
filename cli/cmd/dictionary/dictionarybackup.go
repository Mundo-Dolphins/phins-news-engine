package dictionary

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysession"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarywiring"
)

// The wrappers below keep the names the bot's `dictionary update` command
// imports from this package; the logic lives in dictionarywiring.

// BackupPreviousDictionary preserves the outgoing dictionary version before
// it is overwritten (see dictionarywiring.BackupPreviousDictionary), stamped
// with the current time; errors carry the "dictionary update: " prefix.
func BackupPreviousDictionary(ctx context.Context, db *sql.DB, previous dictionary.Dictionary, diff string, decisions []dictionarysession.Decision) error {
	if err := dictionarywiring.BackupPreviousDictionary(ctx, db, previous, diff, decisions, time.Now()); err != nil {
		return fmt.Errorf("dictionary update: %w", err)
	}
	return nil
}

// SourceEntitiesFromDictionary delegates to
// dictionarywiring.SourceEntitiesFromDictionary.
func SourceEntitiesFromDictionary(d dictionary.Dictionary) []dictionaryupdate.SourceEntity {
	return dictionarywiring.SourceEntitiesFromDictionary(d)
}

// RenderDiffText delegates to dictionarywiring.RenderDiffText.
func RenderDiffText(w io.Writer, proposals []dictionaryupdate.Proposal) error {
	return dictionarywiring.RenderDiffText(w, proposals)
}
