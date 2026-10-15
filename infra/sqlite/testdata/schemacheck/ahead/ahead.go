// Package ahead is known's fixture plus one extra migration, simulating
// a snapshot produced by a newer build than the "known" fixture
// represents.
package ahead

import "embed"

//go:embed *.sql
var FS embed.FS
