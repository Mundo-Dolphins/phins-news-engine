package dictionarywiring

import (
	"context"
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestMineNoCompletedSession(t *testing.T) {
	db := migratedDB(t)
	_, err := Mine(context.Background(), db, &config.Config{}, "")
	if !errors.Is(err, ErrNoCompletedSession) {
		t.Fatalf("error = %v, want ErrNoCompletedSession", err)
	}
	var setup *MineSetupError
	if !errors.As(err, &setup) {
		t.Errorf("error %v is not a *MineSetupError", err)
	}
}
