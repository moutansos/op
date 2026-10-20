//go:build linux

package git

import (
	"os"
	"os/exec"
	"sync"
)

// defaultExecutableSelector resolves each git executable once and returns
// its absolute path. Under WSL, PATH holds dozens of Windows directories on
// slow drive mounts, so looking git.exe up by name costs ~17 ms of CPU and
// ~90 ms per launch.
func defaultExecutableSelector() func(string) string {
	var once sync.Once
	var roots []string
	linuxGit, windowsGit := linuxGitExecutable, WindowsGitExecutable
	return func(directory string) string {
		once.Do(func() {
			if path, err := exec.LookPath(linuxGitExecutable); err == nil {
				linuxGit = path
			}
			mounts, err := os.ReadFile("/proc/self/mounts")
			if err != nil {
				return
			}
			candidates := windowsDriveMounts(mounts)
			if len(candidates) == 0 {
				return
			}
			path, err := exec.LookPath(WindowsGitExecutable)
			if err != nil {
				return
			}
			roots, windowsGit = candidates, path
		})
		if selectExecutable(directory, roots) == WindowsGitExecutable {
			return windowsGit
		}
		return linuxGit
	}
}
