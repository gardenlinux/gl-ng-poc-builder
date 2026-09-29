package log

import (
	"errors"
	"sync"
)

// ErrNoMore is returned by BufferReader.Read when all currently buffered
// records have been consumed. It is NOT a permanent condition — more records
// may arrive later.
var ErrNoMore = errors.New("no more records available")

// BufferTarget is a Target that stores all emitted records in memory.
// It supports multiple concurrent writers (serialized via mutex) and
// arbitrary many readers, each tracking their own position.
type BufferTarget struct {
	mu      sync.Mutex
	cond    *sync.Cond
	records []Record
}

// NewBufferTarget creates a new in-memory buffered log target.
func NewBufferTarget() *BufferTarget {
	bt := &BufferTarget{}
	bt.cond = sync.NewCond(&bt.mu)
	return bt
}

// Emit appends a record to the buffer and notifies waiting readers.
func (bt *BufferTarget) Emit(r Record) {
	bt.mu.Lock()
	bt.records = append(bt.records, r)
	bt.mu.Unlock()
	bt.cond.Broadcast()
}

// Len returns the current number of buffered records.
func (bt *BufferTarget) Len() int {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return len(bt.records)
}

// Reader creates a new BufferReader starting at position 0.
func (bt *BufferTarget) Reader() *BufferReader {
	return &BufferReader{buf: bt}
}

// BufferReader reads records sequentially from a BufferTarget.
// Each reader maintains its own independent read position.
type BufferReader struct {
	buf *BufferTarget
	pos int
}

// Read returns the next record from the buffer. If the reader has caught up
// with all currently buffered records, it returns ErrNoMore. After more
// records are written, Read will return them — ErrNoMore is not permanent.
func (r *BufferReader) Read() (Record, error) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()

	if r.pos >= len(r.buf.records) {
		return Record{}, ErrNoMore
	}
	rec := r.buf.records[r.pos]
	r.pos++
	return rec, nil
}

// Wait blocks until new records are available beyond the reader's current
// position, or until the buffer receives a broadcast (e.g. from Notify).
// Returns true if there are records to read, false if woken without new data
// (caller should re-check or handle stop conditions).
func (r *BufferReader) Wait() {
	r.buf.mu.Lock()
	for r.pos >= len(r.buf.records) {
		r.buf.cond.Wait()
	}
	r.buf.mu.Unlock()
}

// Notify wakes all waiting readers. Used to unblock readers that are waiting
// for more data (e.g., to signal them to check a stop condition).
func (bt *BufferTarget) Notify() {
	bt.cond.Broadcast()
}
