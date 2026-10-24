package cli

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStateIdentityPersistsAcrossConcurrentStarts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var wait sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			id, err := stateInstanceID("")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wait.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if len(id) != 32 || id != first {
			t.Fatalf("inconsistent identity: %q / %q", first, id)
		}
	}
	if id, err := stateInstanceID("other-instance"); err != nil || id != "other-instance" {
		t.Fatalf("explicit identity: %q %v", id, err)
	}
	if id, err := stateInstanceID(""); err != nil || id != first {
		t.Fatalf("restart identity: %q %v", id, err)
	}
}

func TestStateIdentityRejectsEmptyPersistedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "op"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "op", "instance-id"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := stateInstanceID(""); err == nil {
		t.Fatal("empty persisted identity accepted")
	}
}
