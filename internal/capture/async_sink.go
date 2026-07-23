package capture

import (
	"fmt"
	"sync"
	"time"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
)

const (
	asyncSinkQueueSize  = 256
	asyncSinkMaxRetries = 3
)

type workKind int

const (
	workBlob workKind = iota
	workRef
	workFlush
)

type workItem struct {
	kind workKind
	// blob
	hash store.Hash
	data []byte
	// ref
	refName string
	refOld  store.Hash
	refNew  store.Hash
	// flush
	done chan struct{}
}

// AsyncRemoteSink pushes locally-committed objects to a remote server via a
// background goroutine. The caller is never blocked: if the queue is full or
// the server is unreachable the error is logged and the item is dropped.
// Local writes already succeeded before the sink is called, so the agent turn
// is unaffected by any remote failure.
type AsyncRemoteSink struct {
	rem       remote.Remote
	logRoot   string
	queue     chan workItem
	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
}

// NewAsyncRemoteSink creates a sink that replicates to rem.
// logRoot is the .regent/ root used for error logging; may be empty.
func NewAsyncRemoteSink(rem remote.Remote, logRoot string) *AsyncRemoteSink {
	s := &AsyncRemoteSink{
		rem:     rem,
		logRoot: logRoot,
		queue:   make(chan workItem, asyncSinkQueueSize),
		closed:  make(chan struct{}),
	}
	s.wg.Add(1)
	go s.run()
	return s
}

// EnqueueBlob implements CaptureSink. Non-blocking.
func (s *AsyncRemoteSink) EnqueueBlob(hash store.Hash, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case s.queue <- workItem{kind: workBlob, hash: hash, data: cp}:
	case <-s.closed:
	default:
		if s.logRoot != "" {
			LogHookError(s.logRoot, fmt.Sprintf("remote sink queue full, dropping blob %s", hash))
		}
	}
}

// EnqueueRef implements CaptureSink. Non-blocking.
func (s *AsyncRemoteSink) EnqueueRef(name string, old, new store.Hash) {
	select {
	case s.queue <- workItem{kind: workRef, refName: name, refOld: old, refNew: new}:
	case <-s.closed:
	default:
		if s.logRoot != "" {
			LogHookError(s.logRoot, fmt.Sprintf("remote sink queue full, dropping ref %s", name))
		}
	}
}

// Flush implements CaptureSink. Blocks until all currently-queued items are processed.
func (s *AsyncRemoteSink) Flush() error {
	done := make(chan struct{})
	select {
	case s.queue <- workItem{kind: workFlush, done: done}:
		<-done
	case <-s.closed:
	}
	return nil
}

// Close implements CaptureSink. Signals the background goroutine to stop and
// waits for it to drain remaining items. Idempotent.
func (s *AsyncRemoteSink) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	s.wg.Wait()
	return nil
}

func (s *AsyncRemoteSink) run() {
	defer s.wg.Done()
	for {
		select {
		case item := <-s.queue:
			s.process(item)
		case <-s.closed:
			// Drain remaining items before exiting.
			for {
				select {
				case item := <-s.queue:
					s.process(item)
				default:
					return
				}
			}
		}
	}
}

func (s *AsyncRemoteSink) process(item workItem) {
	switch item.kind {
	case workBlob:
		s.pushBlob(item.hash, item.data)
	case workRef:
		s.pushRef(item.refName, item.refOld, item.refNew)
	case workFlush:
		if item.done != nil {
			close(item.done)
		}
	}
}

func (s *AsyncRemoteSink) pushBlob(hash store.Hash, data []byte) {
	exists, err := s.rem.HasObject(hash)
	if err != nil {
		s.logErr(fmt.Sprintf("remote sink HasObject %s: %v", hash, err))
		return
	}
	if exists {
		return
	}
	for attempt := 0; attempt < asyncSinkMaxRetries; attempt++ {
		if sendErr := s.rem.SendObject(hash, data); sendErr == nil {
			return
		} else if attempt < asyncSinkMaxRetries-1 {
			time.Sleep(sinkBackoff(attempt))
		} else {
			s.logErr(fmt.Sprintf("remote sink SendObject %s: %v", hash, sendErr))
		}
	}
}

func (s *AsyncRemoteSink) pushRef(name string, old, new store.Hash) {
	if err := s.rem.UpdateRef(name, old, new); err != nil {
		// CAS conflicts are expected when concurrent sessions push the same ref.
		s.logErr(fmt.Sprintf("remote sink UpdateRef %s: %v", name, err))
	}
}

func (s *AsyncRemoteSink) logErr(msg string) {
	if s.logRoot != "" {
		LogHookError(s.logRoot, msg)
	}
}

func sinkBackoff(attempt int) time.Duration {
	d := 50 * time.Millisecond
	for i := 0; i < attempt; i++ {
		d *= 2
	}
	return d
}
