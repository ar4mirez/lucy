package gateway

import "io"

// NewScrubberForTest exposes the scrubber to the black-box tests.
func NewScrubberForTest(r io.Reader, secret []byte) io.Reader {
	return newScrubber(r, [][]byte{secret})
}
