package docker

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// LogLine is one line of container output.
type LogLine struct {
	Stream string `json:"stream"` // "stdout" or "stderr"
	Text   string `json:"text"`
}

// LogReader splits a container log stream into lines. For containers
// without a TTY the daemon multiplexes stdout and stderr into frames with an
// 8-byte header: [stream, 0, 0, 0, size (uint32 big-endian)].
type LogReader struct {
	r       *bufio.Reader
	tty     bool
	pending []LogLine
	partial map[string]string
}

// NewLogReader wraps a stream from ContainerLogs; tty is the container's
// Config.Tty.
func NewLogReader(r io.Reader, tty bool) *LogReader {
	return &LogReader{r: bufio.NewReaderSize(r, 64<<10), tty: tty, partial: map[string]string{}}
}

const maxLine = 16 << 10 // longer lines are cut, to bound memory

// Next returns the next line, or io.EOF at the end of the stream.
func (l *LogReader) Next() (LogLine, error) {
	for len(l.pending) == 0 {
		if err := l.fill(); err != nil {
			// Flush whatever was left without a trailing newline.
			for _, s := range []string{"stdout", "stderr"} {
				if t := l.partial[s]; t != "" {
					l.pending = append(l.pending, LogLine{Stream: s, Text: t})
					delete(l.partial, s)
				}
			}
			if len(l.pending) == 0 {
				return LogLine{}, err
			}
		}
	}
	line := l.pending[0]
	l.pending = l.pending[1:]
	return line, nil
}

func (l *LogReader) fill() error {
	if l.tty {
		b, err := l.r.ReadSlice('\n')
		if len(b) > 0 {
			l.add("stdout", string(b))
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil
		}
		return err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(l.r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return io.EOF
		}
		return err
	}
	stream := "stdout"
	if hdr[0] == 2 {
		stream = "stderr"
	}
	size := binary.BigEndian.Uint32(hdr[4:])
	buf := make([]byte, size)
	if _, err := io.ReadFull(l.r, buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return io.EOF
		}
		return err
	}
	l.add(stream, string(buf))
	return nil
}

func (l *LogReader) add(stream, chunk string) {
	text := l.partial[stream] + chunk
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			l.pending = append(l.pending, LogLine{Stream: stream, Text: trimCR(text[start:i])})
			start = i + 1
		}
	}
	rest := text[start:]
	if len(rest) > maxLine {
		l.pending = append(l.pending, LogLine{Stream: stream, Text: rest[:maxLine]})
		rest = ""
	}
	l.partial[stream] = rest
}

func trimCR(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\r' {
		return s[:n-1]
	}
	return s
}
