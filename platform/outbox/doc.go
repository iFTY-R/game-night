// Package outbox defines durable events and independent consumer-offset state.
//
// The package owns validation and state transitions only. Dispatch loops, database
// queries, object storage, and transport concerns belong to infrastructure layers.
package outbox
