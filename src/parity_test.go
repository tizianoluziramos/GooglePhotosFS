package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Hashes computed by running the upstream request builders at 1ee56f51.
func TestReferenceRequests(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		hash string
	}{
		{"initial state", libraryState(""), "652959c5a15c096def5bdda3900d8adec931ef4c523046a52a7fd9b9a784ee48"},
		{"delta state", libraryState("test-state"), "67b0653f9c8851bc9b6ebd36ef28cc7bc3f83b77d9645b11cdc329113719a636"},
		{"initial page", libraryInitialPage("test-page"), "e37e97fbaf75df4c984ac734097b4bf0f91469a1c00d6df1a65ac83aaeab11d1"},
		{"delta page", libraryDeltaPage("test-page", "test-state"), "3738e3f612f908a29b2b3ffe45aea1f3bdeaba786715d65972f41b855bd7dd93"},
		{"empty initial cursor", libraryInitialPage(""), "ddf80034c298604c9676484abba4b472a654ae89683ddb443283f497d0760d0f"},
		{"empty delta cursors", libraryDeltaPage("", ""), "ddf80034c298604c9676484abba4b472a654ae89683ddb443283f497d0760d0f"},
		{"hash", checkHashRequest([]byte{1, 2, 3, 4}), "52d1f4021738bb997bff073ac7a5f284c1f2e6407613e16de1d1811f9227b671"},
		{"download", downloadRequest("opaque-key"), "a998acc2b1045d6e7d45e6c9ab25d8c473e678e35673d1973c371b7d769dde51"},
		{"clear description", message(text(2, ""), text(3, "dedup")), "10daff564f3a4ba4877c1524e47ac08419539cf173e06ba66da9eddcc1321bd5"},
		{"set description", message(text(2, "captión"), text(3, "dedup")), "541142581717a8bd8a5fdf531b499ec50cdbbdfa9aa8a86cb41c0d0d3028267a"},
		{"trash", trashRequest([]string{"first", "second"}, 49029607, 35), "669a13e82a68ffca02181333e338bcd781a6e7942ef1bda04af1267a5338b3bf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if hash := fmt.Sprintf("%x", sha256.Sum256(tt.got)); hash != tt.hash {
				t.Fatalf("wire hash %s want %s", hash, tt.hash)
			}
		})
	}
}

func TestParseLibraryResponse(t *testing.T) {
	taken := time.Date(2022, 4, 7, 8, 9, 0, 0, time.UTC)
	meta := message(text(4, "image.jpg"), text(3, "Descripción"), v(7, uint64(taken.UnixMilli())), v(9, uint64(taken.Add(time.Hour).UnixMilli())), v(10, 4096),
		b(13, message(b(1, []byte{1, 2, 3}))), b(31, message(v(1, 1))), b(30, message(v(1, 3))))
	exif := message(text(1, "Google"), text(2, "Pixel"), v(4, uint64(math.Float32bits(1.8))), v(6, 125))
	photo := message(v(1, 1), b(2, message(b(1, message(b(9, message(v(1, 4032), v(2, 3024), b(5, exif))))))))
	latitude := int32(-348000000)
	gps := message(v(1, uint64(uint32(latitude))), v(2, 100000000))
	entry := message(text(1, "item"), b(2, meta), b(5, photo), b(17, message(b(1, gps))))
	gone := message(b(1, message(v(1, 1), b(2, message(text(1, "old-item"))))))
	raw := message(b(1, message(text(1, "page"), b(2, entry), text(6, "state"), b(9, gone))))
	reply, err := parsePage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if reply.State != "state" || reply.Next != "page" || !slices.Equal(reply.Deleted, []string{"old-item"}) || len(reply.Items) != 1 {
		t.Fatalf("%+v", reply)
	}
	item := reply.Items[0]
	want := map[string]string{"description": "Descripción", "media-type": "photo", "width": "4032", "height": "3024", "latitude": "-34.8", "longitude": "10",
		"camera-make": "Google", "camera-model": "Pixel", "iso": "125", "taken-at": "2022-04-07T08:09:00Z", "uploaded-at": "2022-04-07T09:09:00Z",
		"is-favorite": "true", "is-archived": "false", "origin": "partner"}
	for k, v := range want {
		if got := item.metadata()[k]; got != v {
			t.Errorf("%s: %q want %q", k, got, v)
		}
	}
	if item.Size != 4096 || item.SHA1 != "010203" || item.Dedup != "AQID" || !item.Canonical {
		t.Fatalf("%+v", item)
	}
	if _, err = parsePage([]byte{0x0a, 0xff}); err == nil {
		t.Fatal("malformed wire accepted")
	}
}

func TestCatalogLayouts(t *testing.T) {
	enc, _ := parseEncoding("Slash,Dot,CrLf,InvalidUtf8")
	stamp := time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC).UnixMilli()
	items := []Media{
		{Key: "shadow", Dedup: "one", Name: "shadow.jpg", Taken: stamp},
		{Key: "a", Dedup: "one", Name: "test.jpg", Taken: stamp, Canonical: true, Caption: `caption {paths:["/vacation","/favorite","/"]}`},
		{Key: "b", Dedup: "two", Name: "TEST.jpg", Taken: stamp, Canonical: true, Caption: `[path:/vacation]`},
	}
	for mode, first := range map[string]string{
		"flat": "test.jpg", "calendar": "2024/2024-03/2024-03-02/test.jpg", "year": "2024/test.jpg",
		"month": "2024/2024-03/test.jpg", "description_path": "vacation/test.jpg",
	} {
		t.Run(mode, func(t *testing.T) {
			c := project(items, mode, "", enc.windows(), true)
			if got := c.nodes[fold(first)]; got.Media == nil || got.Media.Key != "a" {
				t.Fatalf("%+v", got)
			}
			second := strings.TrimSuffix(first, "test.jpg") + "TEST_two.jpg"
			if got := c.nodes[fold(second)]; got.Media == nil || got.Media.Key != "b" {
				t.Fatalf("missing %s", second)
			}
			if mode == "description_path" {
				if got := c.nodes[fold("favorite/test.jpg")]; got.Media == nil || got.Media.Key != "a" {
					t.Fatal("missing second path")
				}
				filtered := project(items, mode, "vacation", enc.windows(), true)
				if len(filtered.children[""]) != 2 {
					t.Fatalf("children: %+v", filtered.children)
				}
			}
		})
	}
	for _, alias := range []string{"calendar", "calender", "day", "week", "CALENDAR"} {
		if canonicalMode(alias) != "calendar" {
			t.Fatal(alias)
		}
	}
}
func TestVirtualCaptionEscapes(t *testing.T) {
	folders := []string{"/a,b", "/x\"y", "/a]}z", "/"}
	value := withPaths("my caption", folders)
	caption, paths := taggedCaption(value)
	if caption != "my caption" || !slices.Equal(paths, folders) {
		t.Fatalf("%q, %v", caption, paths)
	}
}
func TestNativeEncoding(t *testing.T) {
	enc, _ := parseEncoding("Slash,Dot,CrLf,InvalidUtf8")
	enc = enc.windows()
	for _, name := range []string{"a/b\\c:<>?*\"|. ", "‛．", "‛full", "x\t\ny", "x" + string([]byte{255}), "\x00", "␀"} {
		if got := enc.decode(enc.encode(name)); got != name {
			t.Fatalf("%q round trip to %q", name, got)
		}
	}
	if name := nativeComponent("COM1.jpg", enc); name == "COM1.jpg" {
		t.Fatal("reserved Windows name")
	}
	if name := nativeComponent(strings.Repeat("a", 300)+".jpg", enc); len(name) != 255 || !strings.HasSuffix(name, ".jpg") {
		t.Fatal(name)
	}
}
func TestTrailingQuoteParity(t *testing.T) {
	enc, err := parseEncoding("RightPeriod")
	if err != nil {
		t.Fatal(err)
	}
	if got := enc.decode("clip‛‛．"); got != "clip‛．" {
		t.Fatalf("rclone 1.75.2 decoded as %q", got)
	}
	if got := enc.decodeName("clip‛‛．"); got != "clip‛." {
		t.Fatalf("native input decoded as %q", got)
	}
}

func TestSQLiteCursorAndMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library #1.db")
	c, err := openCache(path, "alice")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	idle, err := c.cursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := idle
	first.state, first.page = "state", "page"
	item := Media{Key: "item", Name: "a.jpg", Dedup: "dedup", Size: 245, Favorite: true, Canonical: true, Latitude: -34.8, SHA1: "aa"}
	if err = c.apply(ctx, idle, first, LibraryPage{Items: []Media{item}}); err != nil {
		t.Fatal(err)
	}
	if err = c.apply(ctx, idle, first, LibraryPage{Deleted: []string{"item"}}); !errors.Is(err, errCacheChanged) {
		t.Fatalf("stale cursor: %v", err)
	}
	if got, err := c.media(ctx, "item", ""); err != nil || got != item {
		t.Fatalf("%+v %v", got, err)
	}
	cursor, err := c.cursor(ctx)
	if err != nil || cursor.state != "state" || cursor.page != "page" || cursor.catalogRevision != 1 {
		t.Fatalf("%+v %v", cursor, err)
	}
	item.Trashed = 1
	if err = c.saveMedia(ctx, item, true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.media(ctx, "item", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("trash: %v", err)
	}
	if rows, revision, err := c.list(ctx); err != nil || len(rows) != 0 || revision != 2 {
		t.Fatalf("%+v %d %v", rows, revision, err)
	}
	if err = c.db.Close(); err != nil {
		t.Fatal(err)
	}
	if other, err := openCache(path, "bob"); err == nil {
		other.db.Close()
		t.Fatal("other account opened index")
	}
}

func TestMigratePreviousCache(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.TrimSuffix(mediaSchema, ",sha1_hash TEXT")
	_, err = db.Exec("CREATE TABLE remote_media(" + schema + "); CREATE TABLE state(id INTEGER PRIMARY KEY, state_token TEXT, page_token TEXT, init_complete INTEGER); INSERT INTO state VALUES(1,'s','',1); INSERT INTO remote_media(media_key,file_name) VALUES('old','older.jpg')")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	c, err := openCache(filename, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer c.db.Close()
	item, err := c.media(context.Background(), "old", "")
	if err != nil || item.Name != "older.jpg" || item.SHA1 != "" {
		t.Fatalf("%+v %v", item, err)
	}
}
