# Flag asset generator

The tray embeds a small fixed-size atlas generated from Twemoji's `assets/72x72`
flag PNGs. The generator uses only the Go standard library and does not run at
application startup.

To refresh the pinned source, download the source tree and run from the
repository root:

```text
go run tools/flagsgen/main.go `
  -src path/to/twemoji/assets/72x72 `
  -out internal/tray/assets/flags.dat `
  -codes internal/tray/flags_codes.go
```

The checked-in atlas is 18x12 pixels per flag. The source and attribution are
recorded in `internal/tray/assets/NOTICE-TWEMOJI.txt`.
