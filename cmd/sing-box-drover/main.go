package main

import (
	"fmt"
	"os"

	"sing-box-drover/internal/app"
	"sing-box-drover/internal/tray"
	platform "sing-box-drover/internal/windows"
)

func main() {
	controller, err := app.New(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sing-box-drover:", err)
		platform.ShowError("sing-box-drover", err.Error())
		return
	}
	if err := tray.Run(controller); err != nil {
		_ = controller.Close()
		fmt.Fprintln(os.Stderr, "sing-box-drover:", err)
		platform.ShowError("sing-box-drover", err.Error())
	}
}
