//go:build linux

package git

import (
	"os"
	"os/exec"
	"sync"
)

func defaultExecutableSelector() func(string) string {
	var once sync.Once
	var roots []string
	return func(directory string) string {
		once.Do(func() {
			mounts, err := os.ReadFile("/proc/self/mounts")
			if err != nil {
				return
			}
			candidates := windowsDriveMounts(mounts)
			if len(candidates) == 0 {
				return
			}
			if _, err := exec.LookPath(WindowsGitExecutable); err != nil {
				return
			}
			roots = candidates
		})
		return selectExecutable(directory, roots)
	}
}
