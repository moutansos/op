//go:build !linux

package process

import "os/exec"

func configureProcessCancellation(*exec.Cmd) {}
