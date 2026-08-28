package server

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRFBClientFilterEnforcesServerSideViewOnly(t *testing.T) {
	filter := newRFBClientFilter()
	handshake := bytes.Repeat([]byte{0x42}, rfbVNCAuthClientHandshakeBytes)
	forwarded, err := filter.Filter(handshake, false)
	if err != nil || !bytes.Equal(forwarded, handshake) {
		t.Fatalf("handshake was not forwarded: %x %v", forwarded, err)
	}

	framebufferRequest := []byte{3, 0, 0, 0, 0, 0, 0, 10, 0, 10}
	keyEvent := []byte{4, 1, 0, 0, 0, 0, 0, 65}
	pointerEvent := []byte{5, 1, 0, 1, 0, 2}
	clipboard := []byte{6, 0, 0, 0, 0, 0, 0, 3, 's', 'e', 'c'}
	setEncodings := []byte{2, 0, 0, 1, 0, 0, 0, 0}
	all := append(append(append(append(append([]byte{}, framebufferRequest...), keyEvent...), pointerEvent...), clipboard...), setEncodings...)
	forwarded, err = filter.Filter(all, false)
	want := append(append([]byte{}, framebufferRequest...), setEncodings...)
	if err != nil || !bytes.Equal(forwarded, want) {
		t.Fatalf("observer forwarding = %x, want %x (%v)", forwarded, want, err)
	}
}

func TestRFBClientFilterHandlesFragmentationAndLeaseTransition(t *testing.T) {
	filter := newRFBClientFilter()
	_, _ = filter.Filter(make([]byte, rfbVNCAuthClientHandshakeBytes), false)
	keyEvent := []byte{4, 1, 0, 0, 0, 0, 0, 65}
	forwarded, err := filter.Filter(keyEvent[:3], false)
	if err != nil || len(forwarded) != 0 {
		t.Fatalf("fragmented observer input leaked: %x %v", forwarded, err)
	}
	forwarded, err = filter.Filter(keyEvent[3:], true)
	if err != nil || !bytes.Equal(forwarded, keyEvent) {
		t.Fatalf("control transition lost input: %x %v", forwarded, err)
	}
}

func TestRFBClientFilterDropsExtendedClipboardAndRejectsUnknownMessages(t *testing.T) {
	filter := newRFBClientFilter()
	_, _ = filter.Filter(make([]byte, rfbVNCAuthClientHandshakeBytes), false)
	extended := make([]byte, 12)
	extended[0] = 6
	negativeLength := int32(-4)
	binary.BigEndian.PutUint32(extended[4:8], uint32(negativeLength))
	if forwarded, err := filter.Filter(extended, false); err != nil || len(forwarded) != 0 {
		t.Fatalf("extended clipboard was not blocked: %x %v", forwarded, err)
	}
	if _, err := filter.Filter([]byte{99}, false); err == nil {
		t.Fatal("unknown RFB message type was accepted")
	}
}
