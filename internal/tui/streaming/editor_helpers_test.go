package streaming

import (
	"io"
	"testing"
)

// makePipe returns a connected read/write pair backed by io.Pipe
// so tests can stream input to the editor in chunks.
func makePipe(t *testing.T) (r io.ReadCloser, w io.WriteCloser) {
	t.Helper()
	return io.Pipe()
}
