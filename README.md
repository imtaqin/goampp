# GoAMPP

A tiny, native Windows XAMPP-style control panel written in Go + [windigo](https://github.com/rodrigocfd/windigo).

- Native Win32 widgets (no CGO, no webview, no Electron)
- Single `.exe`, ~2-3 MB
- Start / Stop / Config buttons for Apache and MySQL
- Real-time log viewer
- Port collision detection (won't start Apache if :80 is busy)

## Folder layout

```
goampp/
├── goampp.exe           # built binary (you build this)
├── main.go              # UI (windigo)
├── service.go           # process manager
├── bin/
│   ├── apache/httpd.exe # drop Apache here
│   ├── mysql/mysqld.exe # drop MySQL here
│   └── php/             # drop PHP here (optional)
├── conf/
│   ├── apache/*.conf    # Apache configs (opened by the "Config" button)
│   └── mysql/*.ini
├── logs/                # service log output
└── www/                 # document root (htdocs)
```

## Requirements

- **Go 1.21+ (64-bit)**. windigo uses constants that overflow 32-bit `int`, so
  a 386 toolchain won't compile it natively. If you have 32-bit Go installed,
  you can still cross-compile by setting `GOARCH=amd64` (the `build.bat` script
  already does this).

## Build

```cmd
build.bat
```

or manually:

```cmd
set GOARCH=amd64
go build -ldflags="-H windowsgui -s -w" -trimpath -o goampp.exe .
```

Optional: shrink further with [UPX](https://upx.github.io):

```cmd
upx --best goampp.exe
```

Typical sizes: ~2.7 MB stripped → ~1 MB after UPX.

## Adding Apache / MySQL

1. Download the Windows binaries (e.g. from Apache Lounge, MariaDB).
2. Extract into `bin/apache/` and `bin/mysql/`.
3. Put configs under `conf/apache/httpd.conf` and `conf/mysql/my.ini`.
4. Run `goampp.exe`.

## Adding more services

Edit `main.go` — the `services` slice near the top of `main()`. Each service
is just:

```go
&Service{
    Name:    "PostgreSQL",
    ExePath: filepath.Join(baseDir, "bin", "pgsql", "bin", "postgres.exe"),
    Args:    []string{"-D", "../data"},
    Port:    5432,
    WorkDir: filepath.Join(baseDir, "bin", "pgsql", "bin"),
}
```

Add a matching row in the layout loop — done.

## Notes

- Binding port 80 usually needs **administrator privileges**. Right-click
  `goampp.exe` → Run as administrator, or bundle a manifest that forces UAC
  elevation.
- `os.Exit(0)` from "Stop all && quit" hard-kills the process. The stop
  routine `Process.Kill()`s children first, but Apache/MySQL may leave stale
  pid files; that's normal.
- The log viewer re-sets the full buffer on each append (simple but fine for
  a control panel). Swap to `EM_SETSEL` + `EM_REPLACESEL` if you plan to log
  megabytes.
