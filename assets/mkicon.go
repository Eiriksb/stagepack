//go:build ignore

// mkicon draws the program icon (a presentation screen with a speaker-notes
// strip) and writes it as assets/stagepack.ico with 256, 48, 32 and 16 px
// images. Original artwork, no third-party parts. Run: go run assets/mkicon.go
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const big = 1024 // drawn this large, then reduced: smooth edges at every size

func lerp(a, b uint8, t float64) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }

// inRound reports whether (x, y) is inside the rounded rectangle [x0,x1]x[y0,y1] with corner radius r.
func inRound(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Min(math.Max(x, x0+r), x1-r)
	cy := math.Min(math.Max(y, y0+r), y1-r)
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

func draw() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, big, big))
	set := func(x, y int, c color.NRGBA) { img.SetNRGBA(x, y, c) }
	for y := 0; y < big; y++ {
		for x := 0; x < big; x++ {
			fx, fy := float64(x), float64(y)
			switch {
			case !inRound(fx, fy, 32, 32, 992, 992, 190):
				// transparent
			case inRound(fx, fy, 150, 190, 874, 640, 56):
				// the slide: white with a title bar, text lines and a picture block
				c := color.NRGBA{250, 251, 253, 255}
				switch {
				case fy > 250 && fy < 330 && fx > 220 && fx < 560:
					c = color.NRGBA{30, 64, 175, 255}
				case fy > 380 && fy < 420 && fx > 220 && fx < 520, fy > 460 && fy < 500 && fx > 220 && fx < 470, fy > 540 && fy < 580 && fx > 220 && fx < 500:
					c = color.NRGBA{148, 163, 184, 255}
				case fx > 600 && fx < 810 && fy > 370 && fy < 580:
					c = color.NRGBA{245, 158, 11, 255}
				}
				set(x, y, c)
			case inRound(fx, fy, 150, 700, 874, 840, 40):
				// the notes strip under the slide
				c := color.NRGBA{226, 232, 240, 255}
				if fy > 748 && fy < 792 && fx > 200 && fx < 700 {
					c = color.NRGBA{71, 85, 105, 255}
				}
				set(x, y, c)
			default:
				t := fy / big // background: deep blue to indigo
				set(x, y, color.NRGBA{lerp(23, 79, t), lerp(37, 70, t), lerp(84, 229, t), 255})
			}
		}
	}
	return img
}

// scale reduces src to n×n by averaging (premultiplied, so edges stay clean).
func scale(src *image.NRGBA, n int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	k := big / n
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a float64
			for j := 0; j < k; j++ {
				for i := 0; i < k; i++ {
					c := src.NRGBAAt(x*k+i, y*k+j)
					al := float64(c.A)
					r, g, b, a = r+float64(c.R)*al, g+float64(c.G)*al, b+float64(c.B)*al, a+al
				}
			}
			if a > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / float64(k*k))})
			}
		}
	}
	return dst
}

func main() {
	sizes := []int{256, 48, 32, 16}
	var pngs [][]byte
	src := draw()
	for _, n := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scale(src, n)); err != nil {
			panic(err)
		}
		pngs = append(pngs, buf.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))}) // ICONDIR
	offset := 6 + 16*len(sizes)
	for i, n := range sizes {
		w := byte(n)
		if n == 256 {
			w = 0 // 0 means 256
		}
		out.Write([]byte{w, w, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32}) // planes, bits per pixel
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(pngs[i])), uint32(offset)})
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	if err := os.WriteFile("assets/stagepack.ico", out.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
