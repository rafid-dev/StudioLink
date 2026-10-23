package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestTrayIcon(t *testing.T) {
	ico := trayIcon(iconActive)
	if !bytes.Equal(ico[:6], []byte{0, 0, 1, 0, 1, 0}) {
		t.Fatalf("ICO header = % x", ico[:6])
	}
	size := binary.LittleEndian.Uint32(ico[14:18])
	offset := binary.LittleEndian.Uint32(ico[18:22])
	if int(offset+size) != len(ico) {
		t.Fatalf("image entry says %d bytes at %d, file is %d", size, offset, len(ico))
	}
	img, err := png.Decode(bytes.NewReader(ico[offset:]))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Errorf("icon is %v", b)
	}
	// Corners are transparent; the center dot and the ring are solid.
	for _, p := range []struct {
		x, y   int
		opaque bool
	}{{0, 0, false}, {15, 15, true}, {15, 2, true}, {15, 8, false}} {
		_, _, _, a := img.At(p.x, p.y).RGBA()
		if (a == 0xffff) != p.opaque {
			t.Errorf("pixel (%d,%d) alpha %#x, want opaque=%v", p.x, p.y, a, p.opaque)
		}
	}
}
