package docker

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"strings"
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
	skip    map[string]bool // dropping the rest of an over-long line
}

// NewLogReader wraps a stream from ContainerLogs; tty is the container's
// Config.Tty.
func NewLogReader(r io.Reader, tty bool) *LogReader {
	return &LogReader{r: bufio.NewReaderSize(r, 64<<10), tty: tty, partial: map[string]string{}, skip: map[string]bool{}}
}

const maxLine = 16 << 10 // longer lines are cut, to bound memory

// truncatedMark ends a line that was cut at maxLine.
const truncatedMark = " …[line truncated]"

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
	for chunk != "" {
		i := strings.IndexByte(chunk, '\n')
		if l.skip[stream] {
			// Inside a line already cut at maxLine: drop up to its end.
			if i < 0 {
				return
			}
			l.skip[stream] = false
			chunk = chunk[i+1:]
			continue
		}
		if i < 0 {
			rest := l.partial[stream] + chunk
			if len(rest) > maxLine {
				// Emit the head once and skip the rest of the line, rather
				// than emitting its later chunks as separate lines.
				l.pending = append(l.pending, LogLine{Stream: stream, Text: rest[:maxLine] + truncatedMark})
				rest = ""
				l.skip[stream] = true
			}
			l.partial[stream] = rest
			return
		}
		line := trimCR(l.partial[stream] + chunk[:i])
		delete(l.partial, stream)
		if len(line) > maxLine {
			line = line[:maxLine] + truncatedMark
		}
		l.pending = append(l.pending, LogLine{Stream: stream, Text: line})
		chunk = chunk[i+1:]
	}
}

func trimCR(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\r' {
		return s[:n-1]
	}
	return s
}
