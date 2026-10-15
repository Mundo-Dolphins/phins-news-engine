package out

import "context"

// UnitOfWork runs fn inside a single database transaction: every
// repository call fn makes through the ctx it receives is committed or
// rolled back atomically together. Use it whenever a use case must persist
// a state change and enqueue a side effect (e.g. an outbox row) as one
// unit — see ADR 0007.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}
