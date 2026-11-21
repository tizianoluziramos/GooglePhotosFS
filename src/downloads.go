package main

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type Downloader interface {
	ReadAt([]byte, int64) (int, error)
	Close() error
}

type Downloads struct {
	photos  *Photos
	enabled bool
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	wg      sync.WaitGroup
	entries map[string]*downloadEntry
	all     map[*downloadEntry]struct{}
}

type downloadEntry struct {
	path     string
	eviction uint64
	file     *os.File
	cancel   context.CancelFunc
	size     int64
	err      error
	done     bool
	changed  chan struct{}
	refs     int
	timer    *time.Timer
}

func newDownloads(api *Photos, enabled bool) *Downloads {
	ctx, cancel := context.WithCancel(context.Background())
	return &Downloads{photos: api, enabled: enabled, ctx: ctx, cancel: cancel, entries: make(map[string]*downloadEntry), all: make(map[*downloadEntry]struct{})}
}

func (d *Downloads) open(item Media) (Downloader, error) {
	if item.Key == "" {
		return nil, errors.New("the media item has no download key")
	}
	if !d.enabled {
		ctx, cancel := context.WithCancel(d.ctx)
		return &streamHandle{photos: d.photos, key: item.Key, ctx: ctx, cancel: cancel}, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}
	id := item.Key
	entry := d.entries[id]
	if entry != nil && entry.done && entry.err != nil {
		delete(d.entries, id)
		if entry.refs == 0 {
			d.forget(entry)
		}
		entry = nil
	}
	if entry == nil {
		file, err := os.CreateTemp("", "googlephotosfs-download-")
		if err != nil {
			return nil, err
		}
		filename := file.Name()
		if os.Remove(filename) == nil {
			filename = ""
		}
		ctx, cancel := context.WithCancel(d.ctx)
		entry = &downloadEntry{file: file, path: filename, cancel: cancel, changed: make(chan struct{})}
		d.entries[id] = entry
		d.all[entry] = struct{}{}
		d.wg.Add(1)
		go d.run(ctx, entry, id)
	}
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	entry.eviction++
	entry.refs++
	return &cacheHandle{owner: d, entry: entry, id: id}, nil
}

// forget is called only after the transfer stops, under d.mu.
func (d *Downloads) forget(e *downloadEntry) {
	if _, tracked := d.all[e]; !tracked {
		return
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	e.cancel()
	e.file.Close()
	if e.path != "" {
		os.Remove(e.path)
	}
	delete(d.all, e)
}

func (d *Downloads) run(ctx context.Context, e *downloadEntry, key string) {
	defer d.wg.Done()
	err := func() error {
		body, err := d.photos.download(ctx, key)
		if err != nil {
			return err
		}
		defer body.Close()
		buf := make([]byte, 256*1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				if _, writeErr := e.file.Write(buf[:n]); writeErr != nil {
					return writeErr
				}
				d.mu.Lock()
				e.size += int64(n)
				close(e.changed)
				e.changed = make(chan struct{})
				d.mu.Unlock()
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}()
	d.mu.Lock()
	e.err, e.done = err, true
	close(e.changed)
	if e.refs == 0 && d.entries[key] != e {
		d.forget(e)
	}
	d.mu.Unlock()
}

type cacheHandle struct {
	owner  *Downloads
	entry  *downloadEntry
	id     string
	closed atomic.Bool
}

func (h *cacheHandle) ReadAt(p []byte, offset int64) (int, error) {
	if h.closed.Load() {
		return 0, os.ErrClosed
	}
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	d := h.owner
	e := h.entry
	for {
		d.mu.Lock()
		available := e.size - offset
		done, err, changed := e.done, e.err, e.changed
		d.mu.Unlock()
		if available >= int64(len(p)) {
			return e.file.ReadAt(p, offset)
		}
		if done {
			n := 0
			if available > 0 {
				n, _ = e.file.ReadAt(p[:available], offset)
			}
			if err != nil {
				return n, err
			}
			return n, io.EOF
		}
		select {
		case <-changed:
		case <-d.ctx.Done():
			return 0, d.ctx.Err()
		}
	}
}

func (h *cacheHandle) Close() error {
	if !h.closed.CompareAndSwap(false, true) {
		return nil
	}
	d, e := h.owner, h.entry
	d.mu.Lock()
	defer d.mu.Unlock()
	e.refs--
	if e.refs != 0 {
		return nil
	}
	if d.entries[h.id] != e {
		if e.done {
			d.forget(e)
		}
		return nil
	}
	e.eviction++
	gen := e.eviction
	e.timer = time.AfterFunc(30*time.Second, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.entries[h.id] != e || e.eviction != gen || e.refs != 0 {
			return
		}
		delete(d.entries, h.id)
		if e.done {
			d.forget(e)
		} else {
			e.cancel()
		}
	})
	return nil
}

func (d *Downloads) close() {
	d.cancel()
	d.mu.Lock()
	d.mu.Unlock()
	d.wg.Wait()
	d.mu.Lock()
	defer d.mu.Unlock()
	for e := range d.all {
		d.forget(e)
	}
	clear(d.entries)
}

type streamHandle struct {
	photos *Photos
	key    string
	ctx    context.Context
	cancel context.CancelFunc
	body   io.ReadCloser
	pos    int64
	mu     sync.Mutex
}

func (h *streamHandle) ReadAt(p []byte, offset int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	if h.body == nil || offset < h.pos {
		if h.body != nil {
			h.body.Close()
		}
		h.body = nil
		body, err := h.photos.download(h.ctx, h.key)
		if err != nil {
			return 0, err
		}
		h.body, h.pos = body, 0
	}
	if gap := offset - h.pos; gap > 0 {
		n, err := io.CopyN(io.Discard, h.body, gap)
		h.pos += n
		if err != nil {
			return 0, err
		}
	}
	n, err := io.ReadFull(h.body, p)
	h.pos += int64(n)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		h.body.Close()
		h.body = nil
	}
	return n, err
}
func (h *streamHandle) Close() error {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.body != nil {
		return h.body.Close()
	}
	return nil
}
