package ui

import (
	"bytes"
	"encoding/binary"
)

// trayIcon is trayPNG as a 32×32 .ico, which is what the Windows tray takes.
func trayIcon() []byte {
	const n = 32
	p := trayPNG()
	var ico bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&ico, le, [3]uint16{0, 1, 1})                        // reserved, type icon, one image
	ico.Write([]byte{n, n, 0, 0})                                     // width, height, colours, reserved
	binary.Write(&ico, le, [2]uint16{1, 32})                          // planes, bits per pixel
	binary.Write(&ico, le, [2]uint32{uint32(len(p)), uint32(6 + 16)}) // size, offset
	ico.Write(p)
	return ico.Bytes()
}

// runFallback reports false: Windows always has a notification area.
func (t *tray) runFallback(ready func()) bool { return false }
