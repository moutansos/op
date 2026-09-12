package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Publish a fully written identity atomically; concurrent starts reuse the winner.
func stateInstanceID(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	directory = filepath.Join(directory, "op")
	path := filepath.Join(directory, "instance-id")
	read := func() (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(data))
		if id == "" {
			return "", fmt.Errorf("empty op instance identity in %s", path)
		}
		return id, nil
	}
	if id, err := read(); !os.IsNotExist(err) {
		return id, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".instance-id-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		file.Close()
		return "", err
	}
	_, writeErr := file.WriteString(hex.EncodeToString(random[:]) + "\n")
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Link(file.Name(), path); err != nil && !os.IsExist(err) {
		return "", err
	}
	return read()
}
