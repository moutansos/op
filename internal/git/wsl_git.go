package git

import (
	"path"
	"strconv"
	"strings"
)

// WindowsGitExecutable is Git for Windows as reached through WSL interop.
// Linux git is very slow on Windows drives mounted into WSL, cannot use the
// Windows credential manager, and cannot follow worktree links written by
// Git for Windows, so repositories on those drives use this executable.
const WindowsGitExecutable = "git.exe"

const linuxGitExecutable = "git"

func selectExecutable(directory string, windowsRoots []string) string {
	directory = path.Clean(directory)
	for _, root := range windowsRoots {
		if directory == root || strings.HasPrefix(directory, strings.TrimSuffix(root, "/")+"/") {
			return WindowsGitExecutable
		}
	}
	return linuxGitExecutable
}

// isWindowsGit reports whether executable, a name or an absolute WSL path,
// is Git for Windows.
func isWindowsGit(executable string) bool {
	return strings.EqualFold(path.Base(executable), WindowsGitExecutable)
}

// windowsDriveMounts returns mount points of Windows drives from
// /proc/self/mounts content. WSL 1 reports drvfs directly; WSL 2 reports 9p
// with an aname=drvfs option.
func windowsDriveMounts(mounts []byte) []string {
	var roots []string
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		fileSystem, options := fields[2], fields[3]
		isDrive := fileSystem == "drvfs"
		if fileSystem == "9p" {
			for _, option := range strings.Split(options, ",") {
				if option == "aname=drvfs" || strings.HasPrefix(option, "aname=drvfs;") {
					isDrive = true
					break
				}
			}
		}
		if !isDrive {
			continue
		}
		mountPoint := unescapeMountField(fields[1])
		if path.IsAbs(mountPoint) && mountPoint != "/" {
			roots = append(roots, path.Clean(mountPoint))
		}
	}
	return roots
}

func unescapeMountField(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+3 < len(value) {
			if code, err := strconv.ParseUint(value[index+1:index+4], 8, 8); err == nil {
				builder.WriteByte(byte(code))
				index += 3
				continue
			}
		}
		builder.WriteByte(value[index])
	}
	return builder.String()
}
