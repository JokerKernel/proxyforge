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

type blockPrefixWriter struct {
	mu      sync.Mutex
	output  io.Writer
	header  []byte
	started bool
}

func NewLinePrefixWriter(output io.Writer, prefix string) io.Writer {
	if output == nil {
		output = io.Discard
	}
	return &linePrefixWriter{output: output, prefix: []byte(prefix), atLineStart: true}
}

// NewBlockPrefixWriter writes a source header once before forwarding the
// original output unchanged. It is intended for a single command's output.
func NewBlockPrefixWriter(output io.Writer, header string) io.Writer {
	if output == nil {
		output = io.Discard
	}
	return &blockPrefixWriter{output: output, header: []byte(header + "\n")}
}

func (w *blockPrefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	if !w.started {
		if _, err := w.output.Write(w.header); err != nil {
			return 0, err
		}
		w.started = true
	}
	return w.output.Write(p)
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

func PrefixBlock(data []byte, header string) []byte {
	if len(data) == 0 {
		return data
	}
	var output bytes.Buffer
	_, _ = NewBlockPrefixWriter(&output, header).Write(data)
	return output.Bytes()
}
