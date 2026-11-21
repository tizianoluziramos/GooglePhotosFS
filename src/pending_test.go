package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPendingUploadSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	file, err := os.CreateTemp(dir, "upload-")
	if err != nil {
		t.Fatal(err)
	}
	filename := file.Name()
	contents := []byte("bytes durable after writer closes")
	if _, err := file.Write(contents); err != nil {
		t.Fatal(err)
	}
	if drafts, err := openPending(dir); err != nil || len(drafts) != 0 {
		t.Fatalf("an unsealed writer must not replay: %v, %v", drafts, err)
	}
	first := &stage{file: file, name: "Viaje/otoño.jpg", dir: "Viaje", dirty: true}
	if err := first.seal(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	drafts, err := openPending(dir)
	if err != nil || len(drafts) != 1 {
		t.Fatalf("restore: %v, %v", drafts, err)
	}
	draft := drafts[0]
	defer draft.file.Close()
	if draft.name != first.name || draft.dir != first.dir || !draft.sealed || !draft.dirty {
		t.Fatalf("invalid restored stage %q %q", draft.name, draft.dir)
	}
	got, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("payload %q, %v", got, err)
	}
	if err := draft.unseal(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filename + ".json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed record still exists: %v", err)
	}
	if remaining, err := openPending(dir); err != nil || len(remaining) != 0 {
		t.Fatalf("unexpected replay: %v, %v", remaining, err)
	}
}

func TestPendingPreservesDamagedData(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "upload-user-data")
	data := []byte("some unfinished bytes")
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename+".json", []byte(`{"path":"bad.jpg","dir":"","size":200}`), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := openPending(dir)
	if err == nil || len(entries) != 0 {
		t.Fatalf("damage must be reported before any upload: %v, %v", entries, err)
	}
	actual, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(data, actual) {
		t.Fatalf("staged file must be retained: %q, %v", actual, err)
	}
}
