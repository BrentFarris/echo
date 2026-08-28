package server

import (
	"encoding/binary"
	"fmt"
)

const (
	rfbVNCAuthClientHandshakeBytes = 30 // version + security choice + challenge response + ClientInit
	rfbMaximumClientMessageBytes   = 16 << 20
)

// rfbClientFilter keeps observer connections useful for framebuffer requests
// while enforcing view-only control at the Echo bridge. TigerVNC is pinned to
// RFB 3.8 VNCAuth, whose client-side handshake has a deterministic 30 bytes.
// Once initialized, keyboard, pointer, clipboard, resize, and power messages
// are dropped unless the authenticated browser session owns the user lease.
type rfbClientFilter struct {
	handshakeRemaining int
	pending            []byte
}

func newRFBClientFilter() *rfbClientFilter {
	return &rfbClientFilter{handshakeRemaining: rfbVNCAuthClientHandshakeBytes}
}

func (f *rfbClientFilter) Filter(data []byte, inputAllowed bool) ([]byte, error) {
	output := make([]byte, 0, len(data)+len(f.pending))
	if f.handshakeRemaining > 0 {
		count := min(f.handshakeRemaining, len(data))
		output = append(output, data[:count]...)
		data = data[count:]
		f.handshakeRemaining -= count
		if f.handshakeRemaining > 0 {
			return output, nil
		}
	}
	if inputAllowed {
		output = append(output, f.pending...)
		f.pending = nil
		output = append(output, data...)
		return output, nil
	}
	f.pending = append(f.pending, data...)
	if len(f.pending) > rfbMaximumClientMessageBytes {
		return nil, fmt.Errorf("RFB client message exceeds its limit")
	}
	for len(f.pending) > 0 {
		length, input, complete, err := rfbClientMessageLength(f.pending)
		if err != nil {
			return nil, err
		}
		if !complete {
			break
		}
		if !input {
			output = append(output, f.pending[:length]...)
		}
		f.pending = f.pending[length:]
	}
	if len(f.pending) == 0 {
		f.pending = nil
	}
	return output, nil
}

func rfbClientMessageLength(data []byte) (length int, input bool, complete bool, err error) {
	if len(data) == 0 {
		return 0, false, false, nil
	}
	require := func(size int, input bool) (int, bool, bool, error) {
		if size < 1 || size > rfbMaximumClientMessageBytes {
			return 0, input, false, fmt.Errorf("invalid RFB client message length")
		}
		return size, input, len(data) >= size, nil
	}
	switch data[0] {
	case 0: // SetPixelFormat
		return require(20, false)
	case 2: // SetEncodings
		if len(data) < 4 {
			return 0, false, false, nil
		}
		return require(4+4*int(binary.BigEndian.Uint16(data[2:4])), false)
	case 3: // FramebufferUpdateRequest
		return require(10, false)
	case 4: // KeyEvent
		return require(8, true)
	case 5: // PointerEvent or ExtendedPointerEvent
		if len(data) < 2 {
			return 0, true, false, nil
		}
		if data[1]&0x80 != 0 {
			return require(7, true)
		}
		return require(6, true)
	case 6: // ClientCutText, including extended clipboard messages
		if len(data) < 8 {
			return 0, true, false, nil
		}
		raw := binary.BigEndian.Uint32(data[4:8])
		payload := int64(int32(raw))
		if payload < 0 {
			payload = -payload
		}
		if payload > rfbMaximumClientMessageBytes-8 {
			return 0, true, false, fmt.Errorf("RFB clipboard message exceeds its limit")
		}
		return require(8+int(payload), true)
	case 150: // EnableContinuousUpdates
		return require(10, false)
	case 248: // ClientFence
		if len(data) < 9 {
			return 0, false, false, nil
		}
		return require(9+int(data[8]), false)
	case 250: // XVP power control
		return require(4, true)
	case 251: // SetDesktopSize
		if len(data) < 8 {
			return 0, true, false, nil
		}
		return require(8+16*int(data[6]), true)
	case 255: // QEMU extended key event
		return require(12, true)
	default:
		return 0, true, false, fmt.Errorf("unsupported RFB client message type %d", data[0])
	}
}
