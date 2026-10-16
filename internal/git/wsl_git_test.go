package git

import (
	"reflect"
	"testing"
)

func TestWindowsDriveMountsRecognizesWSLDrives(t *testing.T) {
	mounts := []byte(`drivers /usr/lib/wsl/drivers 9p ro,dirsync,nosuid,nodev,noatime,aname=drivers;fmask=222;dmask=222,mmap,access=client 0 0
C:\134 /mnt/c 9p rw,dirsync,noatime,aname=drvfs;path=C:\;uid=1000;gid=1000;symlinkroot=/mnt/,mmap,access=client 0 0
D:\134 /mnt/d 9p rw,dirsync,noatime,aname=drvfs;path=D:\;uid=1000;gid=1000;symlinkroot=/mnt/,mmap,access=client 0 0
E: /windows/dev\040drive drvfs rw,noatime,uid=1000,gid=1000 0 0
none /tmp 9p rw,aname=drvfsextra 0 0
/dev/sdc / ext4 rw,relatime,discard,errors=remount-ro,data=ordered 0 0
`)
	want := []string{"/mnt/c", "/mnt/d", "/windows/dev drive"}
	if got := windowsDriveMounts(mounts); !reflect.DeepEqual(got, want) {
		t.Fatalf("windowsDriveMounts() = %#v, want %#v", got, want)
	}
}

func TestSelectExecutableUsesWindowsGitOnlyBelowDriveMounts(t *testing.T) {
	roots := []string{"/mnt/c", "/mnt/d"}
	tests := map[string]string{
		"/mnt/d":                    WindowsGitExecutable,
		"/mnt/d/source/repos/op":    WindowsGitExecutable,
		"/mnt/d/source/repos/op/":   WindowsGitExecutable,
		"/mnt/dev/project":          linuxGitExecutable,
		"/home/ben/source/repos/op": linuxGitExecutable,
		"/mnt":                      linuxGitExecutable,
	}
	for directory, want := range tests {
		if got := selectExecutable(directory, roots); got != want {
			t.Errorf("selectExecutable(%q) = %q, want %q", directory, got, want)
		}
	}
	if got := selectExecutable("/mnt/d/source", nil); got != linuxGitExecutable {
		t.Fatalf("selectExecutable() without drive mounts = %q", got)
	}
}
