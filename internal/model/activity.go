package model

import "io"

type activityReader struct {
	io.Reader
	activity func()
}

func (r activityReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.activity()
	}
	return n, err
}
