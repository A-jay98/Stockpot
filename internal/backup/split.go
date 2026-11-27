package backup

import (
	"bytes"
	"io"
)

// splitWriter collects its input into parts of at most size bytes and hands
// each full part to emit (which may upload it). Parts are held in memory,
// one at a time, because each is hashed before upload and may be re-sent.
type splitWriter struct {
	size    int
	emit    func(part []byte) error
	buf     bytes.Buffer
	emitted int
}

func newSplitWriter(size int, emit func([]byte) error) *splitWriter {
	w := &splitWriter{size: size, emit: emit}
	w.buf.Grow(size)
	return w
}

func (w *splitWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		room := w.size - w.buf.Len()
		chunk := p
		if len(chunk) > room {
			chunk = p[:room]
		}
		w.buf.Write(chunk)
		p = p[len(chunk):]
		n += len(chunk)
		if w.buf.Len() == w.size {
			if err := w.flush(); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func (w *splitWriter) flush() error {
	if err := w.emit(w.buf.Bytes()); err != nil {
		return err
	}
	w.emitted++
	w.buf.Reset()
	return nil
}

// Close emits the final, possibly short part. A stream always has at least
// one part, even if empty.
func (w *splitWriter) Close() error {
	if w.buf.Len() == 0 && w.emitted > 0 {
		return nil
	}
	return w.flush()
}

var _ io.WriteCloser = (*splitWriter)(nil)
