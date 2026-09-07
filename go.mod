module sing-box-drover

go 1.25.0

// Keep the measured low-memory Windows build baseline until a newer toolchain
// no longer materializes the 32 MiB FIPS entropy scratch buffer.
toolchain go1.25.6

require golang.org/x/sys v0.36.0
