// makeicon converts a source PNG into a Windows .ico file that contains
// multiple sizes (16, 32, 48, 64, 128, 256). Modern Windows accepts PNG
// data directly inside ICO entries, so we just resize the source with
// image.NearestNeighbor for small sizes and wrap each in an ICONDIRENTRY.
//
// Usage:
//
//	go run ./tools/makeicon logo.png logo.ico
//
// This is a one-shot build tool — not compiled into the main binary.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"

	"golang.org/x/image/draw"
)

// iconDir / iconDirEntry mirror the Win32 ICONDIR and ICONDIRENTRY layouts.
// Total: 6 bytes header + 16 bytes per entry.
type iconDir struct {
	Reserved uint16 // must be 0
	Type     uint16 // 1 = ICO, 2 = CUR
	Count    uint16 // number of images
}

type iconDirEntry struct {
	Width    uint8  // 0 means 256
	Height   uint8  // 0 means 256
	Colors   uint8  // 0 when >= 8bpp
	Reserved uint8  // must be 0
	Planes   uint16 // color planes (1 for PNG)
	BitCount uint16 // bits per pixel (32 for PNG)
	DataSize uint32 // size of the image data in bytes
	Offset   uint32 // offset from start of file to image data
}

// targetSizes picks a reasonable spread: 16/32 for classic tray, 48 for
// the title bar, 64/128/256 for high-DPI contexts. More sizes = crisper
// rendering without scaling, at a small cost in file size.
var targetSizes = []int{16, 32, 48, 64, 128, 256}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: makeicon <input.png> <output.ico>")
		os.Exit(2)
	}
	srcPath, dstPath := os.Args[1], os.Args[2]

	// Decode the source PNG.
	f, err := os.Open(srcPath)
	must(err)
	defer f.Close()
	src, err := png.Decode(f)
	must(err)

	// Generate one resized PNG per target size.
	entries := make([][]byte, 0, len(targetSizes))
	for _, sz := range targetSizes {
		resized := resizeTo(src, sz, sz)
		var buf bytes.Buffer
		must(png.Encode(&buf, resized))
		entries = append(entries, buf.Bytes())
	}

	// Compose the ICO: header, entry table, image data blocks.
	var out bytes.Buffer
	must(binary.Write(&out, binary.LittleEndian, iconDir{
		Reserved: 0,
		Type:     1, // ICO
		Count:    uint16(len(entries)),
	}))

	// The offset for the first image = 6-byte header + N*16-byte entries.
	offset := uint32(6 + 16*len(entries))
	for i, data := range entries {
		sz := targetSizes[i]
		var w, h uint8
		if sz >= 256 {
			w, h = 0, 0 // 0 = 256 in the ICO format
		} else {
			w, h = uint8(sz), uint8(sz)
		}
		must(binary.Write(&out, binary.LittleEndian, iconDirEntry{
			Width:    w,
			Height:   h,
			Colors:   0,
			Reserved: 0,
			Planes:   1,
			BitCount: 32,
			DataSize: uint32(len(data)),
			Offset:   offset,
		}))
		offset += uint32(len(data))
	}
	// Image blobs follow in the same order as the entries.
	for _, data := range entries {
		out.Write(data)
	}

	must(os.WriteFile(dstPath, out.Bytes(), 0o644))
	fmt.Printf("wrote %s: %d sizes, %d bytes total\n", dstPath, len(entries), out.Len())
}

// resizeTo scales src to w×h using a Catmull-Rom kernel — crisp for
// downscales (our main case: 600×600 source → 16, 32, …).
func resizeTo(src image.Image, w, h int) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
