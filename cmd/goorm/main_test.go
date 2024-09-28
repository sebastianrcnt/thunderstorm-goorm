package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigureLogOutputCreatesLogFile(t *testing.T) {
	dir := t.TempDir()
	logFile, err := configureLogOutput(dir)
	if err != nil {
		t.Fatalf("configureLogOutput returned error: %v", err)
	}
	if logFile == nil {
		t.Fatal("configureLogOutput returned nil file")
	}
	defer logFile.Close()

	if _, err := os.Stat(filepath.Join(dir, "goorm.log")); err != nil {
		t.Fatalf("goorm.log was not created: %v", err)
	}
}
