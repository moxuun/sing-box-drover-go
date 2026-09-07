//go:build !windows

package tray

import (
	"errors"

	"sing-box-drover/internal/app"
)

func Run(*app.App) error { return errors.New("the tray UI is only available on Windows") }
