package outbox

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestExpireApplyErrorMapsNotExpirableToConflict(t *testing.T) {
	err := expireApplyError(fmt.Errorf("sqlite: expire outbox item 7: %w", appout.ErrNotExpirable))
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeConflict {
		t.Fatalf("err = %v, want conflict", err)
	}
	other := errors.New("boom")
	if expireApplyError(other) != other {
		t.Errorf("other errors must pass through")
	}
}
