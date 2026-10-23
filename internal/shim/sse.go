package shim

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// readSSE reads a text/event-stream body and calls emit with the data of
// each event. Comments (keepalives) and other fields are ignored.
func readSSE(body io.Reader, emit func([]byte)) error {
	r := bufio.NewReader(body)
	var data bytes.Buffer
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 || err == nil {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				if data.Len() > 0 {
					emit(bytes.Clone(data.Bytes()))
					data.Reset()
				}
			case line[0] == ':':
			case bytes.HasPrefix(line, []byte("data:")):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.Write(bytes.TrimPrefix(line[len("data:"):], []byte(" ")))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// An event left without its blank line still counts.
				if data.Len() > 0 {
					emit(data.Bytes())
				}
				return nil
			}
			return err
		}
	}
}
