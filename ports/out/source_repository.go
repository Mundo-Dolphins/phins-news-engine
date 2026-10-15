package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// SourceRepository persists the configured content sources.
type SourceRepository interface {
	Save(ctx context.Context, s source.Source) error
	List(ctx context.Context) ([]source.Source, error)
}
