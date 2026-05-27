// makelogos converts service logo PNGs into small .ico files for the
// Services ListView. Source order:
//
//  1. icon/<LocalName>.png    — user-curated logos (preferred)
//  2. <fallbackURL>            — downloaded from the web when (1) is missing
//
// Output goes into assets/icons/<service>.ico. Each .ico contains a
// single 32-pixel PNG entry — small, transparent, and Windows Vista+
// accepts PNG-inside-ICO without complaint.
//
// Run once at build time:
//
//	go run ./tools/makelogos
//
// The generated .ico files are committed to the repo so end-user
// installs don't need a network round trip.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/image/draw"
)

// logo describes one service-to-logo mapping.
//
// localName is the filename (without extension) in the user's icon/
// folder. If that file exists we use it. Otherwise we fall back to
// downloading fallbackURL.
//
// outName is what we write under assets/icons/.
type logo struct {
	service     string
	localName   string // resolved as icon/<localName>.png
	fallbackURL string // used if the local file is missing
	outName     string
}

var logos = []logo{
	// ----- Services (long-running processes) -----
	{
		service:     "Apache",
		localName:   "Apache",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/1/10/Apache_HTTP_server_logo_%282019-present%29.svg/500px-Apache_HTTP_server_logo_%282019-present%29.svg.png",
		outName:     "apache.ico",
	},
	{
		service:     "Nginx",
		localName:   "NGINX",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/c/c5/Nginx_logo.svg/330px-Nginx_logo.svg.png",
		outName:     "nginx.ico",
	},
	{
		service:     "PHP",
		localName:   "PHP",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/2/27/PHP-logo.svg/330px-PHP-logo.svg.png",
		outName:     "php.ico",
	},
	{
		service:     "MySQL",
		localName:   "MySQL",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/c/ca/MariaDB_colour_logo.svg/500px-MariaDB_colour_logo.svg.png",
		outName:     "mysql.ico",
	},
	{
		service:     "PostgreSQL",
		localName:   "PostgresSQL",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/2/29/Postgresql_elephant.svg/256px-Postgresql_elephant.svg.png",
		outName:     "postgresql.ico",
	},
	{
		service:     "Redis",
		localName:   "Redis",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/e/ee/Redis_logo.svg/960px-Redis_logo.svg.png",
		outName:     "redis.ico",
	},
	{
		service:     "phpMyAdmin",
		localName:   "PhpMyAdmin",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/4/4f/PhpMyAdmin_logo.svg/960px-PhpMyAdmin_logo.svg.png",
		outName:     "phpmyadmin.ico",
	},
	{
		service:     "Adminer",
		localName:   "Adminer",
		fallbackURL: "https://raw.githubusercontent.com/vrana/adminer/master/adminer/static/logo.png",
		outName:     "adminer.ico",
	},
	{
		service:     "pgweb",
		localName:   "pgweb",
		fallbackURL: "https://raw.githubusercontent.com/sosedoff/pgweb/master/static/img/favicon.png",
		outName:     "pgweb.ico",
	},
	{
		service:     "MinIO",
		localName:   "s3",
		fallbackURL: "https://raw.githubusercontent.com/minio/minio/master/docs/logo/minio_logo.png",
		outName:     "minio.ico",
	},
	{
		service:     "Mailpit",
		localName:   "mailpit",
		fallbackURL: "https://raw.githubusercontent.com/axllent/mailpit/develop/server/ui/notification.png",
		outName:     "mailpit.ico",
	},

	// ----- Frameworks (scaffoldable projects) -----
	{
		service:     "Laravel",
		localName:   "Laravel",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/9/9a/Laravel.svg/512px-Laravel.svg.png",
		outName:     "laravel.ico",
	},
	{
		service:     "Livewire",
		localName:   "Livewire",
		fallbackURL: "https://raw.githubusercontent.com/livewire/livewire/main/art/logo.png",
		outName:     "livewire.ico",
	},
	{
		service:     "WordPress",
		localName:   "WordPress",
		fallbackURL: "https://s.w.org/style/images/wp-header-logo.png",
		outName:     "wordpress.ico",
	},

	// ----- Runtimes (language / platform) -----
	{
		service:     "Erlang",
		localName:   "erlang",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Erlang.png",
		outName:     "erlang.ico",
	},
	{
		service:     "Julia",
		localName:   "julia",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Julia.png",
		outName:     "julia.ico",
	},
	{
		service:     "Zig",
		localName:   "Zig",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Zig.png",
		outName:     "zig.ico",
	},
	{
		service:     "Dart",
		localName:   "Dart",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Dart.png",
		outName:     "dart.ico",
	},
	{
		service:     "Idris2",
		localName:   "idris2",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Idris2.png",
		outName:     "idris2.ico",
	},
	{
		service:     "Lua",
		localName:   "Lua",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Lua.png",
		outName:     "lua.ico",
	},
	{
		service:     "Ruby",
		localName:   "Ruby",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Ruby.png",
		outName:     "ruby.ico",
	},
	{
		service:     "Rust",
		localName:   "Rust",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Rust.png",
		outName:     "rust.ico",
	},
	{
		service:     "Kotlin",
		localName:   "Kotlin",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Kotlin.png",
		outName:     "kotlin.ico",
	},
	{
		service:     "Haskell",
		localName:   "Haskell",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Haskell.png",
		outName:     "haskell.ico",
	},
	{
		service:     "Elixir",
		localName:   "Elixir",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Elixir.png",
		outName:     "elixir.ico",
	},
	{
		service:     "Crystal",
		localName:   "Crystal",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Crystal.png",
		outName:     "crystal.ico",
	},
	{
		service:     "Scala",
		localName:   "Scala",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Scala.png",
		outName:     "scala.ico",
	},
	{
		service:     "Swift",
		localName:   "Swift",
		fallbackURL: "https://icon.icepanel.io/Technology/png-shadow-512/Swift.png",
		outName:     "swift.ico",
	},
	{
		service:     "Node.js",
		localName:   "Node.js",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/d/d9/Node.js_logo.svg/512px-Node.js_logo.svg.png",
		outName:     "nodejs.ico",
	},
	{
		service:     "AdonisJS",
		localName:   "AdonisJS",
		fallbackURL: "https://raw.githubusercontent.com/adonisjs/art/master/png/logo.png",
		outName:     "adonisjs.ico",
	},
	{
		service:     "Go",
		localName:   "Go",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/0/05/Go_Logo_Blue.svg/512px-Go_Logo_Blue.svg.png",
		outName:     "go.ico",
	},
	{
		service:     "Java",
		localName:   "Java",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/en/thumb/3/30/Java_programming_language_logo.svg/512px-Java_programming_language_logo.svg.png",
		outName:     "java.ico",
	},
	{
		service:     "Python",
		localName:   "Python", // user doesn't have this one — will download
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/c/c3/Python-logo-notext.svg/512px-Python-logo-notext.svg.png",
		outName:     "python.ico",
	},
	{
		service:     "Composer",
		localName:   "Composer",
		fallbackURL: "https://upload.wikimedia.org/wikipedia/commons/thumb/8/81/Composer-logo.svg/512px-Composer-logo.svg.png",
		outName:     "composer.ico",
	},
}

// ICO file format (single-entry, PNG payload).
type iconDir struct {
	Reserved uint16
	Type     uint16
	Count    uint16
}

type iconDirEntry struct {
	Width    uint8
	Height   uint8
	Colors   uint8
	Reserved uint8
	Planes   uint16
	BitCount uint16
	DataSize uint32
	Offset   uint32
}

// 32 pixels covers the LVSIL_SMALL slot at any DPI we care about.
const iconSize = 32

func main() {
	outDir := filepath.Join("assets", "icons")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		die("mkdir: " + err.Error())
	}

	// Wipe so stale icons from an older run don't leak into the build.
	// Users can still keep their PNGs under icon/ unchanged.
	for _, l := range logos {
		_ = os.Remove(filepath.Join(outDir, l.outName))
	}

	client := &http.Client{Timeout: 20 * time.Second}
	ok := 0
	for _, l := range logos {
		outPath := filepath.Join(outDir, l.outName)

		var (
			src    image.Image
			source string
			err    error
		)

		// Prefer the local PNG if the user dropped one in icon/.
		localPath := filepath.Join("icon", l.localName+".png")
		if _, statErr := os.Stat(localPath); statErr == nil {
			src, err = readLocalPNG(localPath)
			source = "local"
		} else {
			src, err = downloadImage(client, l.fallbackURL)
			source = "web"
		}

		if err != nil {
			fmt.Printf("%-12s FAILED: %v\n", l.service, err)
			continue
		}

		ico, err := encodeIco(src, iconSize)
		if err != nil {
			fmt.Printf("%-12s FAILED encode: %v\n", l.service, err)
			continue
		}
		if err := os.WriteFile(outPath, ico, 0o644); err != nil {
			fmt.Printf("%-12s FAILED write: %v\n", l.service, err)
			continue
		}
		fmt.Printf("%-12s %s → %s (%dx%d, %d bytes)\n",
			l.service, source, l.outName,
			src.Bounds().Dx(), src.Bounds().Dy(), len(ico))
		ok++
	}

	fmt.Printf("\n%d/%d icons written to %s/\n", ok, len(logos), outDir)
}

func readLocalPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func downloadImage(client *http.Client, url string) (image.Image, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Wikimedia rejects empty UAs.
	req.Header.Set("User-Agent", "GoAMPP-makelogos/0.1 (+https://github.com/goampp)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(buf))
	return img, err
}

// encodeIco resizes src to a square `size`-pixel PNG and wraps it in
// the minimal ICO header.
func encodeIco(src image.Image, size int) ([]byte, error) {
	// Center the source in a square canvas so non-square PNGs
	// (like the Nginx wordmark at 330×70) don't get horribly
	// squashed. We scale the larger dimension to fit size and
	// center along the smaller one.
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	scale := float64(size) / float64(sw)
	if float64(sh)*scale > float64(size) {
		scale = float64(size) / float64(sh)
	}
	dw := int(float64(sw) * scale)
	dh := int(float64(sh) * scale)
	offX := (size - dw) / 2
	offY := (size - dh) / 2

	canvas := image.NewNRGBA(image.Rect(0, 0, size, size))
	dst := image.Rect(offX, offY, offX+dw, offY+dh)
	draw.CatmullRom.Scale(canvas, dst, src, src.Bounds(), draw.Over, nil)

	// PNG-encode the canvas.
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, canvas); err != nil {
		return nil, err
	}

	// Wrap in a single-entry ICO file.
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, iconDir{
		Reserved: 0, Type: 1, Count: 1,
	})
	var w, h uint8
	if size >= 256 {
		w, h = 0, 0 // 0 means 256 in the ICO format
	} else {
		w, h = uint8(size), uint8(size)
	}
	_ = binary.Write(&out, binary.LittleEndian, iconDirEntry{
		Width: w, Height: h, Colors: 0, Reserved: 0,
		Planes: 1, BitCount: 32,
		DataSize: uint32(pngBuf.Len()),
		Offset:   uint32(6 + 16),
	})
	out.Write(pngBuf.Bytes())
	return out.Bytes(), nil
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}
