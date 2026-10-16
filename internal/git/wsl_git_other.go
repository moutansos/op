//go:build !linux

package git

func defaultExecutableSelector() func(string) string {
	return func(string) string { return linuxGitExecutable }
}
