package main

import (
	"bytes"
	"compress/gzip"
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
	"sync/atomic"
	"testing"
	"time"
)

// The local HTTP fixture exercises the production client and SQLite workflow
// with deterministic responses. It never talks to a live Google account.
type fixtureTransport struct {
	url  *url.URL
	base http.RoundTripper
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.URL.Scheme, copy.URL.Host = f.url.Scheme, f.url.Host
	copy.Header.Set("X-Test-Origin", r.URL.Host)
	return f.base.RoundTrip(copy)
}

func TestMobileTransferWorkflow(t *testing.T) {
	payload := []byte("bytes for the transfer test")
	digest := sha1.Sum(payload)
	dedup := base64.RawURLEncoding.EncodeToString(digest[:])
	var mu sync.Mutex
	var caption string
	var uploaded, trashed bool
	var uploads, commits, downloads, refreshes atomic.Int32
	out := func(w http.ResponseWriter, data []byte) {
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		z.Write(data)
		z.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/auth") {
			values, err := url.ParseQuery(string(data))
			if err != nil || values.Get("app") != photosApp || values.Get("Token") != "test-device-token" {
				t.Error("auth request", err)
			}
			refreshes.Add(1)
			out(w, []byte(fmt.Sprintf("Auth=test-bearer\nExpiry=%d", time.Now().Add(time.Hour).Unix())))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-bearer" {
			t.Errorf("authorization = %q", got)
		}
		raw, _ := decode(data)
		switch "https://" + r.Header.Get("X-Test-Origin") + r.URL.Path {
		case libraryEndpoint:
			mu.Lock()
			defer mu.Unlock()
			stamp := time.Date(2024, 5, 6, 1, 2, 3, 0, time.UTC).UnixMilli()
			meta := message(text(4, "photo.jpg"), text(3, caption), v(7, uint64(stamp)), v(10, uint64(len(payload))), b(13, message(b(1, digest[:]))))
			fields := message(text(6, "state"), b(4, message(b(5, message(v(1, 1))), b(8, message(v(1, 20), v(2, 100))))))
			if uploaded && !trashed {
				fields = append(fields, b(2, message(text(1, "media"), b(2, meta), b(5, message(v(1, 1)))))...)
			}
			if trashed {
				fields = append(fields, b(9, message(b(1, message(v(1, 1), b(2, message(text(1, "media")))))))...)
			}
			out(w, message(b(1, fields)))
		case hashEndpoint:
			if got := raw.sub(1, 1).first(1).bytes; !bytes.Equal(got, digest[:]) {
				t.Error("incorrect digest")
			}
			mu.Lock()
			defer mu.Unlock()
			var response []byte
			if uploaded {
				found := message(b(2, message(text(1, "media"))))
				response = message(b(1, message(b(2, found))))
			}
			out(w, response)
		case uploadEndpoint:
			switch r.Method {
			case http.MethodPost:
				if r.Header.Get("X-Goog-Hash") != "sha1="+base64.StdEncoding.EncodeToString(digest[:]) || raw.num(7) != int64(len(payload)) {
					t.Error("incorrect upload session properties")
				}
				w.Header().Set("X-GUploader-UploadID", "upload/test")
			case http.MethodPut:
				uploads.Add(1)
				if !bytes.Equal(data, payload) || r.URL.Query().Get("upload_id") != "upload/test" {
					t.Error("upload payload or session differs")
				}
				out(w, []byte("opaque upload response"))
			default:
				t.Errorf("method %s", r.Method)
				w.WriteHeader(405)
			}
		case commitEndpoint:
			commits.Add(1)
			if m := raw.sub(1); m.str(1) != "opaque upload response" || m.str(2) != "photo.jpg" || m.num(7) != 3 || !bytes.Equal(m.first(3).bytes, digest[:]) {
				t.Error("incorrect commit properties")
			}
			if device := raw.sub(2); device.str(3) != "Pixel XL" || device.num(5) != 28 {
				t.Error("incorrect device profile")
			}
			mu.Lock()
			uploaded = true
			mu.Unlock()
			out(w, message(b(1, message(b(3, message(text(1, "media")))))))
		case captionEndpoint:
			mu.Lock()
			caption = raw.str(2)
			mu.Unlock()
			if raw.str(3) != dedup {
				t.Error("caption dedup mismatch")
			}
			out(w, nil)
		case trashEndpoint:
			mu.Lock()
			trashed = true
			mu.Unlock()
			if raw.str(3) != dedup {
				t.Error("trash dedup mismatch")
			}
			out(w, nil)
		case downloadEndpoint:
			if raw.sub(1, 1).str(1) != "media" {
				t.Error("wrong download key")
			}
			data := message(text(6, "https://photos.googleusercontent.com/original"), text(5, "https://photos.googleusercontent.com/edited"))
			out(w, message(b(1, message(b(5, message(b(2, data)))))))
		case "https://photos.googleusercontent.com/original":
			downloads.Add(1)
			w.Write(payload)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	config := Config{Photos: PhotoConfig{AuthData: "androidId=abcd&Email=test%40example.com&Token=test-device-token",
		CacheDBPath: filepath.Join(t.TempDir(), "transfer.db"), ListMode: "description_path", Encoding: "Slash,Dot,CrLf,InvalidUtf8", UnlimitedUpload: true, DownloadCache: true}}
	s, err := openStore(config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	origin, _ := url.Parse(server.URL)
	s.photos.client.Transport = fixtureTransport{url: origin, base: server.Client().Transport}
	ctx := context.Background()
	if err = s.sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "photo.jpg")
	if err = os.WriteFile(filename, payload, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	item, err := s.put(ctx, f, "photo.jpg", "trip")
	if err != nil || item.Key != "media" || item.Dedup != dedup {
		t.Fatalf("%+v, %v", item, err)
	}
	if _, err = s.put(ctx, f, "photo.jpg", "favorites"); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 1 || commits.Load() != 1 || refreshes.Load() != 1 {
		t.Fatalf("uploads=%d commits=%d refreshes=%d", uploads.Load(), commits.Load(), refreshes.Load())
	}
	trip, err := s.node("trip/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	favorite, err := s.node("favorites/photo.jpg")
	if err != nil || favorite.Media.Key != trip.Media.Key {
		t.Fatalf("%+v %v", favorite, err)
	}
	a, err := s.downloads.open(*favorite.Media)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	bReader, err := s.downloads.open(*favorite.Media)
	if err != nil {
		t.Fatal(err)
	}
	defer bReader.Close()
	buf := make([]byte, 5)
	if _, err = a.ReadAt(buf, 12); err != nil || !bytes.Equal(buf, payload[12:17]) {
		t.Fatalf("read at 12: %q %v", buf, err)
	}
	if _, err = bReader.ReadAt(buf, 0); err != nil || !bytes.Equal(buf, payload[:5]) {
		t.Fatalf("read at 0: %q %v", buf, err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("%d transfers for shared read", downloads.Load())
	}

	uncached := newDownloads(s.photos, false)
	defer uncached.close()
	stream, err := uncached.open(*favorite.Media)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = stream.ReadAt(buf, 12); err != nil || !bytes.Equal(buf, payload[12:17]) {
		t.Fatalf("stream offset 12: %q %v", buf, err)
	}
	if _, err = stream.ReadAt(buf, 0); err != nil || !bytes.Equal(buf, payload[:5]) {
		t.Fatalf("stream rewind: %q %v", buf, err)
	}
	if downloads.Load() != 3 {
		t.Fatalf("uncached rewind should reopen the stream, transfers=%d", downloads.Load())
	}
	usage, err := s.photos.usage(ctx)
	if err != nil || usage.Free == nil || *usage.Free != 80 || usage.Objects == nil || *usage.Objects != 1 {
		t.Fatalf("%+v %v", usage, err)
	}
	if err = s.mutate(ctx, trip, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.node("trip/photo.jpg"); err == nil {
		t.Fatal("removed link still visible")
	}
	favorite, err = s.node("favorites/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	description := withPaths("new caption", []string{"/favorites"})
	if err = s.mutate(ctx, favorite, &description); err != nil {
		t.Fatal(err)
	}
	favorite, err = s.node("favorites/photo.jpg")
	if err != nil || favorite.Media.Caption != description {
		t.Fatalf("%+v %v", favorite, err)
	}
	if err = s.mutate(ctx, favorite, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.node("favorites/photo.jpg"); err == nil {
		t.Fatal("trashed item still visible")
	}
	if err = s.sync(ctx, true); err != nil {
		t.Fatal(err)
	}
}
