//go:build windows && !amd64 && !arm64

package main

// The Windows proxy intentionally excludes 386. A 32-bit process cannot
// safely use GetSystemDirectory to locate the native 64-bit wsl.exe without
// explicit Sysnative handling.
var _ = opWindowsProxyRequiresAMD64OrARM64
