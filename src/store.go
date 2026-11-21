package main

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
)

var (
	errVirtualDirectory = errors.New("no se puede borrar un directorio virtual")
	errNoDedup          = errors.New("el medio no tiene clave de deduplicación")
	errReadOnly         = errors.New("unidad de solo lectura")
)

type Store struct {
	config      Config
	photos      *Photos
	cache       *Cache
	mode        string
	encoding    nameEncoding
	native      bool
	uploadLocks [64]sync.Mutex
	mutations   *flock.Flock
	catalog     atomic.Pointer[Catalog]
	mu          sync.Mutex
	revision    int64
	downloads   *Downloads
}

func openStore(cfg Config, native bool) (*Store, error) {
	api, err := newPhotos(cfg.Photos)
	if err != nil {
		return nil, err
	}
	enc, err := parseEncoding(cfg.Photos.Encoding)
	if err != nil {
		return nil, err
	}
	if native {
		enc = enc.windows()
	}
	cache, err := openCache(cfg.Photos.CacheDBPath, strings.ToLower(api.credentials.Get("Email")))
	if err != nil {
		return nil, err
	}
	return &Store{config: cfg, photos: api, cache: cache, mode: canonicalMode(cfg.Photos.ListMode),
		encoding: enc, native: native, revision: -1, downloads: newDownloads(api, cfg.Photos.DownloadCache),
		mutations: flock.New(cfg.Photos.CacheDBPath + ".write-lock")}, nil
}
func (s *Store) close() error {
	s.downloads.close()
	s.photos.client.CloseIdleConnections()
	return s.cache.db.Close()
}

func (s *Store) sync(ctx context.Context, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refresh(ctx, force)
}

func (s *Store) refresh(ctx context.Context, force bool) error {
	var syncErr error
	for attempts := 0; attempts < 3; attempts++ {
		syncErr = s.syncPages(ctx, force)
		if !errors.Is(syncErr, errCacheChanged) {
			break
		}
	}
	cur, err := s.cache.cursor(ctx)
	if err != nil {
		return errors.Join(syncErr, err)
	}
	if !cur.complete && syncErr != nil {
		return syncErr
	}
	if cur.catalogRevision != s.revision || s.catalog.Load() == nil {
		items, revision, err := s.cache.list(ctx)
		if err != nil {
			return errors.Join(syncErr, err)
		}
		s.catalog.Store(project(items, s.mode, s.config.Filesystem.Root, s.encoding, s.native))
		s.revision = revision
	}
	return syncErr
}

func (s *Store) syncPages(ctx context.Context, force bool) error {
	cur, err := s.cache.cursor(ctx)
	if err != nil {
		return err
	}
	if cur.complete && cur.page == "" && !force && time.Now().Unix()-cur.last < 30 {
		return nil
	}
	for {
		base := cur.state
		if cur.page != "" && cur.complete && cur.base != "" {
			base = cur.base
		}
		update, err := s.photos.library(ctx, base, cur.page, !cur.complete)
		if err != nil {
			return fmt.Errorf("sincronizar biblioteca: %w", err)
		}
		if update.Next != "" && update.Next == cur.page {
			return errors.New("Google devolvió el mismo cursor de página")
		}
		next := cur
		if update.State != "" && cur.page == "" {
			next.state = update.State
		}
		next.page = update.Next
		if next.page != "" && cur.complete {
			next.base = base
		}
		if next.page == "" {
			next.base = ""
			next.complete = true
			next.last = time.Now().Unix()
		}
		if err = s.cache.apply(ctx, cur, next, update); err != nil {
			return err
		}
		if next.page == "" {
			return nil
		}
		next.revision++
		cur = next
	}
}

func (s *Store) node(name string) (Node, error) {
	if c := s.catalog.Load(); c != nil {
		if node, ok := c.nodes[fold(name)]; ok {
			return node, nil
		}
		// An explicit key suffix remains addressable when the named item is unique.
		ext := path.Ext(name)
		base := strings.TrimSuffix(path.Base(name), ext)
		if i := strings.LastIndexByte(base, '_'); i >= 0 && i+1 < len(base) {
			for _, node := range c.children[fold(folder(name))] {
				if node.Media != nil && (node.Media.Key == base[i+1:] || node.Media.Dedup == base[i+1:]) {
					return node, nil
				}
			}
		}
	}
	return Node{}, os.ErrNotExist
}

func (s *Store) publish(ctx context.Context) error {
	items, revision, err := s.cache.list(ctx)
	if err != nil {
		return err
	}
	s.catalog.Store(project(items, s.mode, s.config.Filesystem.Root, s.encoding, s.native))
	s.revision = revision
	return nil
}

// Serialize caption and cache mutations across the mounted worker and CLI.
func (s *Store) lockMutation(ctx context.Context) error {
	locked, err := s.mutations.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("bloqueo del índice: %w", ctx.Err())
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *Store) put(ctx context.Context, src *os.File, name, dir string) (Media, error) {
	if s.config.Filesystem.ReadOnly {
		return Media{}, errReadOnly
	}
	if err := ctx.Err(); err != nil {
		return Media{}, err
	}
	info, err := src.Stat()
	if err != nil {
		return Media{}, err
	}
	digest := sha1.New()
	if _, err = io.Copy(digest, contextReader{ctx: ctx, reader: io.NewSectionReader(src, 0, info.Size())}); err != nil {
		return Media{}, err
	}
	raw := digest.Sum(nil)
	dedup := base64.RawURLEncoding.EncodeToString(raw)
	uploadLock := &s.uploadLocks[raw[0]%64]
	uploadLock.Lock()
	defer uploadLock.Unlock()
	requestName := name
	if s.native && strings.HasPrefix(name, "‛") && strings.HasPrefix(nativeComponent(name[len("‛"):], s.encoding), "‛") {
		unprefixed := name[len("‛"):]
		if nativeComponent(unprefixed, s.encoding) == name {
			requestName = unprefixed
		}
	}
	decodedName := s.encoding.decode(requestName)
	if s.native {
		decodedName = s.encoding.decodeName(requestName)
	}
	remoteName := standardEncoding.encode(decodedName)
	key, hashErr := s.photos.lookupHash(ctx, raw)
	fresh := hashErr != nil || key == ""
	if fresh {
		key, err = s.photos.upload(ctx, io.NewSectionReader(src, 0, info.Size()), info.Size(), remoteName, raw)
		if err != nil {
			return Media{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.lockMutation(ctx); err != nil {
		return Media{}, err
	}
	defer s.mutations.Unlock()
	// Refresh first so an existing duplicate retains its canonical name and caption.
	syncErr := s.syncPages(ctx, true)
	item, err := s.cache.media(ctx, key, dedup)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Media{}, err
	}
	inIndex := err == nil
	if !inIndex {
		if !fresh && s.mode == "description_path" {
			if syncErr != nil {
				return Media{}, syncErr
			}
			return Media{}, errors.New("Google encontró el duplicado, pero aún no publicó sus metadatos")
		}
		item = Media{Key: key, Dedup: dedup, Name: remoteName, Size: info.Size(), SHA1: hex.EncodeToString(raw), Canonical: true}
	}
	if s.mode == "description_path" {
		body, tags := taggedCaption(item.Caption)
		target := "/" + dir
		if !slices.Contains(tags, target) {
			tags = append(tags, target)
		}
		value := withPaths(body, tags)
		if value != item.Caption {
			if err = s.photos.caption(ctx, item.Dedup, value); err != nil {
				return Media{}, err
			}
			item.Caption = value
		}
	}
	if inIndex || fresh && (s.mode == "description_path" || s.mode == "flat") {
		if err = s.cache.saveMedia(ctx, item, false); err != nil {
			return Media{}, err
		}
	}
	return item, s.publish(ctx)
}

func (s *Store) mutate(ctx context.Context, node Node, description *string) error {
	if s.config.Filesystem.ReadOnly {
		return errReadOnly
	}
	if node.Dir {
		return errVirtualDirectory
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lockMutation(ctx); err != nil {
		return err
	}
	defer s.mutations.Unlock()
	item, err := s.cache.media(ctx, node.Media.Key, node.Media.Dedup)
	if err != nil {
		return err
	}
	if item.Dedup == "" {
		return errNoDedup
	}
	remove := description == nil
	if description != nil {
		item.Caption = *description
	} else if s.mode == "description_path" {
		body, tags := taggedCaption(item.Caption)
		tags = slices.DeleteFunc(tags, func(s string) bool { return cleanVirtual(s) == node.Virt })
		if len(tags) > 0 {
			item.Caption = withPaths(body, tags)
			remove = false
		}
	}
	if remove {
		err = s.photos.trash(ctx, item.Dedup)
		item.Trashed = time.Now().UnixMilli()
	} else {
		err = s.photos.caption(ctx, item.Dedup, item.Caption)
	}
	if err != nil {
		return err
	}
	if err = s.cache.saveMedia(ctx, item, remove); err != nil {
		return err
	}
	return s.publish(ctx)
}

func (m Media) metadata() map[string]string {
	out := map[string]string{"is-favorite": strconv.FormatBool(m.Favorite), "is-archived": strconv.FormatBool(m.Archived)}
	for key, value := range map[string]string{
		"description": m.Caption, "location-name": m.Location, "camera-make": m.Make, "camera-model": m.Model, "origin": m.Origin,
	} {
		if value != "" {
			out[key] = value
		}
	}
	switch m.Kind {
	case 1:
		out["media-type"] = "photo"
	case 2:
		out["media-type"] = "video"
	default:
		if m.Kind != 0 {
			out["media-type"] = strconv.FormatInt(m.Kind, 10)
		}
	}
	for key, value := range map[string]int64{"width": m.Width, "height": m.Height, "duration": m.Duration, "iso": m.ISO} {
		if value > 0 {
			out[key] = strconv.FormatInt(value, 10)
		}
	}
	for key, value := range map[string]float64{"aperture": m.Aperture, "shutter-speed": m.Shutter, "focal-length": m.Focal} {
		if value != 0 {
			out[key] = strconv.FormatFloat(value, 'f', -1, 64)
		}
	}
	if m.Latitude != 0 || m.Longitude != 0 {
		out["latitude"] = strconv.FormatFloat(m.Latitude, 'f', -1, 64)
		out["longitude"] = strconv.FormatFloat(m.Longitude, 'f', -1, 64)
	}
	if m.Taken > 0 {
		out["taken-at"] = time.UnixMilli(m.Taken).UTC().Format(time.RFC3339)
	}
	if m.Uploaded > 0 {
		out["uploaded-at"] = time.UnixMilli(m.Uploaded).UTC().Format(time.RFC3339)
	}
	return out
}
