//go:build windows && amd64

package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type kernelMedia struct {
	name, caption string
	data          []byte
	taken         uint64
}

func (m kernelMedia) sha1() [20]byte { return sha1.Sum(m.data) }

func TestWinFspVolume(t *testing.T) {
	if os.Getenv("GOOGLEPHOTOSFS_TEST_WINFSP") != "1" {
		t.Skip("WinFsp Core is required. Set GOOGLEPHOTOSFS_TEST_WINFSP=1 on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	original := []byte("original test file")
	photos := map[string]kernelMedia{"original": {name: "existing.jpg", data: original, taken: 1000}}
	var transfers map[string][]byte = make(map[string][]byte)
	var tombstones []string
	var mu sync.Mutex
	rejectUpload := false
	sequence := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		m, _ := decode(raw)
		mu.Lock()
		defer mu.Unlock()
		switch "https://" + r.Header.Get("X-Test-Origin") + r.URL.Path {
		case authRefresh:
			fmt.Fprintf(w, "Auth=test-bearer\nExpiry=%d", time.Now().Add(time.Hour).Unix())
		case libraryEndpoint:
			result := message(text(6, "state"), b(4, message(b(8, message(v(1, 100), v(2, 10000))))))
			for id, entry := range photos {
				digest := entry.sha1()
				meta := message(text(4, entry.name), text(3, entry.caption), v(7, entry.taken), v(10, uint64(len(entry.data))), b(13, message(b(1, digest[:]))))
				result = append(result, b(2, message(text(1, id), b(2, meta), b(5, message(v(1, 1)))))...)
			}
			for _, id := range tombstones {
				result = append(result, b(9, message(b(1, message(v(1, 1), b(2, message(text(1, id)))))))...)
			}
			w.Write(message(b(1, result)))
		case downloadEndpoint:
			id := m.sub(1, 1).str(1)
			if _, ok := photos[id]; !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(message(b(1, message(b(5, message(b(2, message(text(6, "https://photos.googleusercontent.com/raw/"+id)))))))))
		case hashEndpoint:
			sha := m.sub(1, 1).first(1).bytes
			for id, entry := range photos {
				digest := entry.sha1()
				if bytes.Equal(sha, digest[:]) {
					match := message(b(2, message(text(1, id))))
					w.Write(message(b(1, message(b(2, match)))))
					return
				}
			}
		case uploadEndpoint:
			if rejectUpload {
				w.WriteHeader(400)
				return
			}
			if r.Method == http.MethodPost {
				sequence++
				w.Header().Set("X-GUploader-UploadID", fmt.Sprintf("session-%d", sequence))
			} else {
				id := r.URL.Query().Get("upload_id")
				transfers[id] = raw
				w.Write([]byte(id))
			}
		case commitEndpoint:
			item := m.sub(1)
			id := item.str(1)
			data, ok := transfers[id]
			if !ok {
				t.Error("commit missing staged bytes")
				w.WriteHeader(400)
				return
			}
			delete(transfers, id)
			photos[id] = kernelMedia{name: item.str(2), data: data, taken: 1000 + uint64(sequence)}
			w.Write(message(b(1, message(b(3, message(text(1, id)))))))
		case trashEndpoint:
			for id, entry := range photos {
				sha := entry.sha1()
				if base64.RawURLEncoding.EncodeToString(sha[:]) == m.str(3) {
					delete(photos, id)
					tombstones = append(tombstones, id)
				}
			}
		default:
			if id, ok := strings.CutPrefix(r.URL.Path, "/raw/"); ok {
				entry, found := photos[id]
				if !found {
					w.WriteHeader(404)
					return
				}
				w.Write(entry.data)
				return
			}
			t.Errorf("unexpected endpoint %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	cfg := Config{dir: root, Photos: PhotoConfig{
		AuthData:    "androidId=abcd&Email=kernel%40example.com&Token=test-device-token",
		CacheDBPath: filepath.Join(root, "index.db"), Encoding: "Slash,Dot,CrLf,InvalidUtf8", ListMode: "flat", DownloadCache: true,
	}}
	s, err := openStore(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	origin, _ := url.Parse(server.URL)
	s.photos.client.Transport = fixtureTransport{url: origin, base: server.Client().Transport}
	if err := s.sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	drives, err := windows.GetLogicalDrives()
	if err != nil {
		t.Fatal(err)
	}
	letter := ""
	for disk := 'Z'; disk >= 'P'; disk-- {
		if drives&(1<<uint(disk-'A')) == 0 {
			letter = string(disk) + ":"
			break
		}
	}
	if letter == "" {
		t.Fatal("could not find a free Windows drive letter")
	}
	drive := letter + `\`
	start := func(backgroundUploads bool) (*Volume, func()) {
		t.Helper()
		volume, err := newVolume(ctx, s, letter)
		if err != nil {
			t.Fatal(err)
		}
		mounted, err := volume.mount(letter)
		if err != nil {
			t.Fatalf("WinFsp mount: %v", err)
		}
		stopUploads := func() {}
		if backgroundUploads {
			stopUploads = volume.startPending(ctx)
		}
		return volume, sync.OnceFunc(func() {
			stopUploads()
			mounted.Unmount()
			_ = volume.shutdown()
		})
	}
	assertContents := func(filename string, expected []byte) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(drive, filename))
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("%s contains %q, want %q. %v", filename, got, expected, err)
		}
	}
	firstVolume, unmount := start(true)
	defer unmount()
	waitForStage := func(volume *Volume, file string, sealed bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			_, pending, err := volume.find(file)
			if err == nil && pending == nil && !sealed {
				return
			}
			if pending != nil && sealed {
				pending.mu.Lock()
				ready := pending.sealed && !pending.committing
				pending.mu.Unlock()
				if ready {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("stage %s didn't reach expected state; sealed=%t", file, sealed)
	}
	names, err := os.ReadDir(drive)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range names {
		if strings.EqualFold(entry.Name(), "existing.jpg") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing fixture in directory entries: %v", names)
	}
	assertContents("EXISTING.jpg", original)
	existing, err := os.OpenFile(filepath.Join(drive, "existing.jpg"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	part := make([]byte, 4)
	if _, err = existing.ReadAt(part, 2); err != nil || !bytes.Equal(part, original[2:6]) {
		t.Fatalf("range read: %q %v", part, err)
	}
	if _, err = existing.WriteAt([]byte("NEW"), 2); err != nil {
		t.Fatal(err)
	}
	if err = existing.Truncate(13); err != nil {
		t.Fatal(err)
	}
	if err = existing.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = existing.Close(); err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), original[:13]...)
	copy(changed[2:], []byte("NEW"))
	waitForStage(firstVolume, "existing.jpg", false)
	assertContents("existing.jpg", changed)

	uploaded := []byte("fresh file payload")
	newName := filepath.Join(drive, "new.jpg")
	if err = os.WriteFile(newName, uploaded, 0600); err != nil {
		t.Fatal(err)
	}
	waitForStage(firstVolume, "new.jpg", false)
	assertContents("new.jpg", uploaded)
	if err = os.Remove(newName); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(newName); !os.IsNotExist(err) {
		t.Fatalf("deleted file: %v", err)
	}

	pending := []byte("upload that survives unmount")
	mu.Lock()
	rejectUpload = true
	mu.Unlock()
	if err = os.WriteFile(filepath.Join(drive, "pending.jpg"), pending, 0600); err != nil {
		t.Fatal(err)
	}
	waitForStage(firstVolume, "pending.jpg", true)
	assertContents("pending.jpg", pending)
	unmount()

	mu.Lock()
	rejectUpload = false
	mu.Unlock()
	volume, unmountAgain := start(false)
	defer unmountAgain()
	assertContents("pending.jpg", pending)
	volume.retryPending(ctx)
	if _, staged, err := volume.find("pending.jpg"); err != nil || staged != nil {
		t.Fatalf("recovery: %v %v", staged, err)
	}
	assertContents("pending.jpg", pending)
	unmountAgain()

	s.config.Filesystem.ReadOnly = true
	_, unmountReadOnly := start(false)
	defer unmountReadOnly()
	assertContents("pending.jpg", pending)
	if err = os.WriteFile(filepath.Join(drive, "blocked.jpg"), []byte("blocked"), 0600); err == nil {
		t.Fatal("write to read-only volume went through")
	}
}
