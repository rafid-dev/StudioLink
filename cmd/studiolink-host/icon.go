package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Tray icon colors: waiting, serving at least one session, and something
// needs attention (a relay is down).
var (
	iconIdle   = color.RGBA{0x3b, 0x82, 0xf6, 0xff}
	iconActive = color.RGBA{0x16, 0xa3, 0x4a, 0xff}
	iconWarn   = color.RGBA{0xd9, 0x77, 0x06, 0xff}
)

// trayIcon draws a 32x32 ring with a center dot in c, as .ico bytes (a PNG
// inside an ICO header, which Windows has accepted since Vista).
func trayIcon(c color.RGBA) []byte {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	center := (size - 1) / 2.0
	for y := range size {
		for x := range size {
			d := math.Hypot(float64(x)-center, float64(y)-center)
			// Coverage with a one-pixel soft edge on each boundary.
			cover := math.Min(edge(d, 15.5), 1-edge(d, 10.5)) // outer ring
			cover = math.Max(cover, edge(d, 5.5))             // center dot
			if cover > 0 {
				img.SetRGBA(x, y, color.RGBA{
					R: uint8(float64(c.R) * cover), G: uint8(float64(c.G) * cover),
					B: uint8(float64(c.B) * cover), A: uint8(255 * cover),
				})
			}
		}
	}
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, img)

	var ico bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&ico, le, [3]uint16{0, 1, 1}) // reserved, type icon, one image
	ico.Write([]byte{size, size, 0, 0})            // width, height, palette, reserved
	_ = binary.Write(&ico, le, [2]uint16{1, 32})   // planes, bits per pixel
	_ = binary.Write(&ico, le, [2]uint32{uint32(pngData.Len()), 22})
	ico.Write(pngData.Bytes())
	return ico.Bytes()
}

// edge is 1 inside radius r, 0 outside, blending over one pixel.
func edge(d, r float64) float64 {
	return math.Max(0, math.Min(1, r-d+0.5))
}
