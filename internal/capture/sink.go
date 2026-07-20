package capture

import "github.com/regent-vcs/regent/internal/store"

// CaptureSink receives locally-committed objects for optional remote replication.
// Every method must be safe for concurrent use.
// EnqueueBlob and EnqueueRef must never block the caller.
type CaptureSink interface {
	// EnqueueBlob queues a blob for replication.
	// Called after the blob is successfully written to the local store.
	EnqueueBlob(hash store.Hash, data []byte)
	// EnqueueRef queues a ref update for replication.
	// Called after the ref is successfully updated in the local store.
	EnqueueRef(name string, old, new store.Hash)
	// Flush blocks until all currently-queued work is done.
	// Safe to call from tests and graceful shutdown paths.
	Flush() error
	// Close flushes and shuts down the sink. Idempotent.
	Close() error
}

// NoopSink discards all events. Used when no remote is configured.
type NoopSink struct{}

func (*NoopSink) EnqueueBlob(_ store.Hash, _ []byte)   {}
func (*NoopSink) EnqueueRef(_ string, _, _ store.Hash) {}
func (*NoopSink) Flush() error                         { return nil }
func (*NoopSink) Close() error                         { return nil }
