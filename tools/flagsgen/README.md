# 旗帜资源生成器

托盘内置一份小型定尺寸图集，由 Twemoji 的 `assets/72x72` 旗帜 PNG 生成。
生成器只使用 Go 标准库，也不会在应用启动时运行。

需要更新固定版本的资源时，下载 Twemoji 源码并在本仓库根目录执行：

```text
go run tools/flagsgen/main.go `
  -src path/to/twemoji/assets/72x72 `
  -out internal/tray/assets/flags.dat `
  -codes internal/tray/flags_codes.go
```

仓库中的图集将每面旗帜保存为 18×12 像素。来源与授权说明记录在
`internal/tray/assets/NOTICE-TWEMOJI.txt`。
