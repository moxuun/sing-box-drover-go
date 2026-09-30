module sing-box-drover

go 1.25.0

// Keep the measured low-memory Windows build baseline on the latest supported
// Go 1.25 patch until a newer major toolchain is measured against it.
toolchain go1.25.14

require (
	fyne.io/systray v1.12.2
	golang.org/x/sys v0.36.0
)

require github.com/godbus/dbus/v5 v5.1.0 // indirect
