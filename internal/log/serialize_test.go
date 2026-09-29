package log

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBufferTargetMarshalJSON(t *testing.T) {
	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "hello"})
	bt.Emit(Record{Level: Error, Component: Importer, Msg: "boom"})

	data, err := bt.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// The wire format must use the level/component string forms (not int
	// codes), so logs serialized today remain readable across iota
	// renumbering. Spot-check a few literals.
	s := string(data)
	for _, want := range []string{`"level":"info"`, `"level":"error"`, `"component":"build"`, `"component":"importer"`, `"msg":"hello"`, `"msg":"boom"`} {
		if !bytes.Contains([]byte(s), []byte(want)) {
			t.Errorf("marshalled JSON missing %q: %s", want, s)
		}
	}
}

// TestBufferTargetMarshalEmpty pins that an empty buffer marshals to a JSON
// array, not null. Tools downstream of the buffer parse the result with a
// `[]Record` decoder; null would deserialize fine but break consumers that
// later range over it without a nil check.
func TestBufferTargetMarshalEmpty(t *testing.T) {
	bt := NewBufferTarget()
	data, err := bt.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if got := string(data); got != "[]" {
		t.Errorf("empty buffer marshalled as %q, want %q", got, "[]")
	}
}

// TestBufferTargetRoundTrip is the central serialize test: marshal a buffer,
// unmarshal into a fresh BufferTarget, and confirm both reader access AND
// concurrent Notify-on-Emit still work after the deserialized cond is
// re-initialized. UnmarshalJSON has a special case (serialize.go:41) to
// re-create the cond when called on a zero-value BufferTarget — if that
// re-init were ever dropped, the next Emit would panic on a nil cond.
func TestBufferTargetRoundTrip(t *testing.T) {
	src := NewBufferTarget()
	src.Emit(Record{Level: Info, Component: Build, Msg: "first"})
	src.Emit(Record{Level: Warn, Component: Importer, Msg: "second"})
	src.Emit(Record{Level: Debug, Component: Container, Msg: "third"})

	data, err := src.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// dst is an unconfigured zero-value BufferTarget: cond is nil before
	// UnmarshalJSON. This is the case Unmarshal must handle.
	var dst BufferTarget
	if err := dst.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if dst.Len() != 3 {
		t.Fatalf("dst.Len() = %d, want 3", dst.Len())
	}

	r := dst.Reader()
	want := []Record{
		{Level: Info, Component: Build, Msg: "first"},
		{Level: Warn, Component: Importer, Msg: "second"},
		{Level: Debug, Component: Container, Msg: "third"},
	}
	for i, w := range want {
		got, err := r.Read()
		if err != nil {
			t.Fatalf("Read[%d]: %v", i, err)
		}
		if got != w {
			t.Errorf("rec[%d] = %+v, want %+v", i, got, w)
		}
	}

	// After unmarshal, the buffer should still be usable: emitting a new
	// record must not panic on the freshly-initialized cond.
	dst.Emit(Record{Level: Error, Component: Engine, Msg: "post-unmarshal"})
	if dst.Len() != 4 {
		t.Errorf("dst.Len() after post-unmarshal Emit = %d, want 4", dst.Len())
	}
}

// TestBufferTargetUnmarshalReplacesContent pins the contract that
// UnmarshalJSON REPLACES existing records rather than appending.
func TestBufferTargetUnmarshalReplacesContent(t *testing.T) {
	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "old"})

	payload, err := json.Marshal([]map[string]any{
		{"level": "warn", "component": "importer", "msg": "new"},
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	if err := bt.UnmarshalJSON(payload); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if bt.Len() != 1 {
		t.Fatalf("Len after Unmarshal = %d, want 1 (Unmarshal must replace, not append)", bt.Len())
	}
	r := bt.Reader()
	rec, err := r.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Msg != "new" || rec.Level != Warn || rec.Component != Importer {
		t.Errorf("rec = %+v, want Warn/Importer/new", rec)
	}
}

func TestBufferTargetUnmarshalInvalidJSON(t *testing.T) {
	var bt BufferTarget
	if err := bt.UnmarshalJSON([]byte("not json")); err == nil {
		t.Fatal("expected error from UnmarshalJSON on garbage input, got nil")
	}
}

func TestBufferTargetUnmarshalUnknownLevel(t *testing.T) {
	var bt BufferTarget
	payload := []byte(`[{"level":"trace","component":"build","msg":"x"}]`)
	if err := bt.UnmarshalJSON(payload); err == nil {
		t.Fatal("expected error for unknown level 'trace', got nil")
	}
}

func TestBufferTargetWriteToReadFrom(t *testing.T) {
	src := NewBufferTarget()
	src.Emit(Record{Level: Info, Component: Build, Msg: "alpha"})
	src.Emit(Record{Level: Debug, Component: Importer, Msg: "beta"})

	var buf bytes.Buffer
	n, err := src.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if n == 0 {
		t.Errorf("WriteTo returned n=0 for non-empty buffer")
	}
	if buf.Len() == 0 {
		t.Fatal("WriteTo produced no bytes")
	}

	dst := NewBufferTarget()
	if _, err := dst.ReadFrom(&buf); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	if dst.Len() != 2 {
		t.Fatalf("dst.Len() = %d, want 2", dst.Len())
	}
	r := dst.Reader()
	rec, _ := r.Read()
	if rec.Msg != "alpha" {
		t.Errorf("first rec msg = %q, want %q", rec.Msg, "alpha")
	}
	rec, _ = r.Read()
	if rec.Msg != "beta" {
		t.Errorf("second rec msg = %q, want %q", rec.Msg, "beta")
	}
}
