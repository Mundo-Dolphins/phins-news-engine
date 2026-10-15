package classification

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func BenchmarkDecide(b *testing.B) {
	dict, err := dictionaryfile.Load(repofs.ExampleConfig(b, "example_dictionary.yml"))
	if err != nil {
		b.Fatalf("load dictionary: %v", err)
	}
	m, err := dictionary.Prepare(dict)
	if err != nil {
		b.Fatalf("prepare matcher: %v", err)
	}
	title := "Isotopes rally past Sharks as Jordan Sample throws three touchdowns"
	body := "Springfield Isotopes quarterback Jordan Sample threw for 310 yards and three touchdowns Sunday as Springfield defeated Shelbyville 27-24 at Example Stadium, keeping pace in the division race behind head coach Morgan Mockley's game plan."

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Decide(title, body, m)
	}
}
