package gateway

import (
	"bytes"
	"io"
)

// Redacted replaces a credential an upstream echoed back.
const Redacted = "<redacted>"

// scrubber streams r with every occurrence of each secret replaced by Redacted, including one
// split across reads, so a token an upstream reflects (an echo endpoint, an error that quotes the
// request) never reaches the worker (LUCY-CRED-005).
type scrubber struct {
	r       io.Reader
	secrets [][]byte
	keep    int // the longest secret, minus one: bytes held back in case a secret continues
	buf     []byte
	out     []byte
	eof     bool
	err     error
	found   int
}

func newScrubber(r io.Reader, secrets [][]byte) *scrubber {
	s := &scrubber{r: r}
	for _, sec := range secrets {
		if len(sec) == 0 {
			continue
		}
		s.secrets = append(s.secrets, sec)
		if len(sec)-1 > s.keep {
			s.keep = len(sec) - 1
		}
	}
	return s
}

func (s *scrubber) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.eof {
			if s.err != nil {
				return 0, s.err
			}
			return 0, io.EOF
		}
		chunk := make([]byte, 32*1024)
		n, err := s.r.Read(chunk)
		s.buf = append(s.buf, chunk[:n]...)
		if err != nil {
			s.eof = true
			if err != io.EOF {
				s.err = err
			}
		}
		s.buf = s.replace(s.buf)
		// Emit all but the tail a secret could still be starting in.
		emit := len(s.buf)
		if !s.eof {
			emit -= s.keep
		}
		if emit > 0 {
			s.out = append(s.out, s.buf[:emit]...)
			s.buf = append(s.buf[:0], s.buf[emit:]...)
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

// replace replaces every whole secret in b.
func (s *scrubber) replace(b []byte) []byte {
	for _, sec := range s.secrets {
		if n := bytes.Count(b, sec); n > 0 {
			s.found += n
			b = bytes.ReplaceAll(b, sec, []byte(Redacted))
		}
	}
	return b
}

// scrubString replaces every secret in v.
func scrubString(v string, secrets [][]byte) (string, int) {
	found := 0
	for _, sec := range secrets {
		if len(sec) == 0 {
			continue
		}
		if n := bytes.Count([]byte(v), sec); n > 0 {
			found += n
			v = string(bytes.ReplaceAll([]byte(v), sec, []byte(Redacted)))
		}
	}
	return v, found
}
