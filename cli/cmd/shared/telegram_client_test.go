package shared_test

import (
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestDefaultTelegramImportClientIsAnExplicitError(t *testing.T) {
	_, err := shared.NewTelegramImportClient(out.TelegramImportClientConfig{})
	if !errors.Is(err, shared.ErrNoTelegramImportClient) {
		t.Fatalf("err = %v, want ErrNoTelegramImportClient", err)
	}
}
