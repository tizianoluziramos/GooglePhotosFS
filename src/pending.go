package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type stage struct {
	mu                                   sync.Mutex
	file                                 *os.File
	name, dir                            string
	dirty                                bool
	writers, refs                        int
	deleted                              bool
	committing                           bool
	sealed                               bool
	retryAt                              time.Time
	attributes                           uint32
	created, accessed, modified, changed uint64
}

type pendingUpload struct {
	Path string `json:"path"`
	Dir  string `json:"dir"`
	Size int64  `json:"size"`
}

func pendingDirectory(s *Store, letter string) string {
	identity := strings.Join([]string{
		strings.ToLower(s.photos.credentials.Get("Email")),
		s.config.Photos.CacheDBPath,
		s.config.Filesystem.Root,
		s.mode,
		s.config.Photos.Encoding,
		strings.ToUpper(letter),
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(s.config.dir, "pending", hex.EncodeToString(digest[:12]))
}

// seal publishes the destination only after all local bytes are durable.
// An interrupted write with no record remains on disk for manual recovery.
func (p *stage) seal() error {
	if p.sealed {
		return nil
	}
	if err := p.file.Sync(); err != nil {
		return err
	}
	info, err := p.file.Stat()
	if err != nil {
		return err
	}
	record, err := json.Marshal(pendingUpload{Path: p.name, Dir: p.dir, Size: info.Size()})
	if err != nil {
		return err
	}
	if err := replaceContents(p.file.Name()+".json", ".pending-", record); err != nil {
		return err
	}
	p.sealed = true
	return nil
}

// unseal runs before changing, deleting or consuming a staged upload.
func (p *stage) unseal() error {
	if !p.sealed {
		return nil
	}
	if err := os.Remove(p.file.Name() + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p.sealed = false
	p.retryAt = time.Time{}
	return nil
}

func openPending(directory string) ([]*stage, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending []*stage
	var invalid []error
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type() != 0 || !strings.HasPrefix(name, "upload-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		p, err := readPending(filepath.Join(directory, name))
		if err != nil {
			invalid = append(invalid, fmt.Errorf("%s: %w", name, err))
			continue
		}
		pending = append(pending, p)
	}
	return pending, errors.Join(invalid...)
}

func readPending(record string) (*stage, error) {
	info, err := os.Lstat(record)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return nil, errors.New("registro de carga demasiado grande")
	}
	data, err := os.ReadFile(record)
	if err != nil {
		return nil, err
	}
	var saved pendingUpload
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if saved.Path == "" || cleanVirtual(saved.Path) != saved.Path || cleanVirtual(saved.Dir) != saved.Dir ||
		strings.ContainsAny(saved.Path, "\x00\\:") || strings.ContainsRune(saved.Dir, 0) || saved.Size < 0 {
		return nil, errors.New("destino de carga inválido")
	}
	filename := strings.TrimSuffix(record, ".json")
	info, err = os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != saved.Size {
		return nil, errors.New("tamaño inesperado de los datos de carga")
	}
	f, err := os.OpenFile(filename, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return &stage{file: f, name: saved.Path, dir: saved.Dir, dirty: true, sealed: true}, nil
}
