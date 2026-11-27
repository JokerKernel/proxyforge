package system

import (
	"bytes"
	"io"
	"sync"
)

type linePrefixWriter struct {
	mu          sync.Mutex
	output      io.Writer
	prefix      []byte
	atLineStart bool
}

func NewLinePrefixWriter(output io.Writer, prefix string) io.Writer {
	if output == nil {
		output = io.Discard
	}
	return &linePrefixWriter{output: output, prefix: []byte(prefix), atLineStart: true}
}

func (w *linePrefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for offset := 0; offset < len(p); {
		if w.atLineStart {
			if _, err := w.output.Write(w.prefix); err != nil {
				return offset, err
			}
			w.atLineStart = false
		}
		relativeEnd := bytes.IndexByte(p[offset:], '\n')
		end := len(p)
		if relativeEnd >= 0 {
			end = offset + relativeEnd + 1
		}
		if _, err := w.output.Write(p[offset:end]); err != nil {
			return offset, err
		}
		w.atLineStart = p[end-1] == '\n'
		offset = end
	}
	return len(p), nil
}

func PrefixLines(data []byte, prefix string) []byte {
	if len(data) == 0 {
		return data
	}
	var output bytes.Buffer
	_, _ = NewLinePrefixWriter(&output, prefix).Write(data)
	return output.Bytes()
}
