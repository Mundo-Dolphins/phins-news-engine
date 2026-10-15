package dictionary

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysession"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const dictionaryHistoryDir = "data/dictionary/history"

// BackupPreviousDictionary preserves the outgoing dictionary version before
// it is overwritten: it writes the full artifact set (dictionary, diff,
// decision log, validation result) for previous under dictionaryHistoryDir
// via dictionaryhistory.WriteVersion, then records the version in the
// append-only dictionary_history SQLite ledger. It does not mark previous as
// promoted — that status moves to the newly written version once the write
// that follows this backup succeeds.
func BackupPreviousDictionary(ctx context.Context, db *sql.DB, previous dictionary.Dictionary, diff string, decisions []dictionarysession.Decision) error {
	validation := dictionary.Validate(previous)
	if err := dictionaryhistory.WriteVersion(dictionaryHistoryDir, dictionaryhistory.Artifacts{
		Version: previous.Version, Dictionary: previous, Diff: diff, Decisions: decisions, Validation: validation,
	}); err != nil {
		return fmt.Errorf("dictionary update: %w", err)
	}

	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: previous.Version, ContentHash: dictionary.ContentHash(previous), CreatedAt: time.Now().UTC(),
		OperationKind: "update",
	}); err != nil {
		return fmt.Errorf("dictionary update: %w", err)
	}
	return nil
}
