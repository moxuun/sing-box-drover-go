package main

import (
	"errors"
	"fmt"
	"os"

	"sing-box-drover/internal/app"
	"sing-box-drover/internal/tray"
	platform "sing-box-drover/internal/windows"
)

func main() {
	platform.EnableDPIAwareness()
	controller, err := app.New(os.Args[1:])
	if err != nil {
		if errors.Is(err, app.ErrElevationHandoff) {
			return
		}
		fmt.Fprintln(os.Stderr, "sing-box-drover:", err)
		platform.ShowError("sing-box-drover", err.Error())
		return
	}
	if err := tray.Run(controller); err != nil {
		if closeErr := controller.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		fmt.Fprintln(os.Stderr, "sing-box-drover:", err)
		platform.ShowError("sing-box-drover", err.Error())
	}
}
