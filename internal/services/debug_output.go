package services

const maxDebugOutputBytes = 1024 * 1024

type debugOutputBuffer struct {
	data []byte
}

func (b *debugOutputBuffer) append(value string) {
	if value == "" {
		return
	}
	bytes := []byte(value)
	if len(bytes) >= maxDebugOutputBytes {
		b.data = append(b.data[:0], bytes[len(bytes)-maxDebugOutputBytes:]...)
		return
	}
	if extra := len(b.data) + len(bytes) - maxDebugOutputBytes; extra > 0 {
		copy(b.data, b.data[extra:])
		b.data = b.data[:len(b.data)-extra]
	}
	b.data = append(b.data, bytes...)
}

func (b *debugOutputBuffer) String() string { return string(b.data) }
