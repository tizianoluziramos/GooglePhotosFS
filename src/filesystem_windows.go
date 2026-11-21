//go:build windows && amd64

package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	wf "github.com/winfsp/go-winfsp"
	"golang.org/x/sys/windows"
)

const (
	dirRequest       = 1
	nonDirRequest    = 0x40
	deleteOnClose    = 0x1000
	dataAccess       = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA
	stagedAttributes = windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM |
		windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_TEMPORARY | windows.FILE_ATTRIBUTE_NOT_CONTENT_INDEXED
)

type fileHandle struct {
	mu              sync.Mutex
	node            Node
	pending         *stage
	reader          Downloader
	dirBuf          wf.DirBuffer
	writer, cleaned bool
}

type Volume struct {
	store       *Store
	ctx         context.Context
	sd          *windows.SECURITY_DESCRIPTOR
	born        time.Time
	staging     string
	pendingWake chan struct{}
	handles     sync.Map
	sequence    atomic.Uint64
	mu          sync.Mutex
	files       map[string]*stage
	dirs        map[string]Node
	usageMu     sync.Mutex
	usage       Usage
	usageAt     time.Time
}

func newVolume(ctx context.Context, s *Store, letter string) (*Volume, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := user.User.Sid.String()
	perms := "FA"
	if s.config.Filesystem.ReadOnly {
		perms = "FRFX"
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "G:" + sid + "D:P(A;;FA;;;SY)(A;;" + perms + ";;;" + sid + ")")
	if err != nil {
		return nil, err
	}
	v := &Volume{store: s, ctx: ctx, sd: sd, born: time.Now(), staging: pendingDirectory(s, letter),
		files: make(map[string]*stage), dirs: make(map[string]Node), pendingWake: make(chan struct{}, 1)}
	if !s.config.Filesystem.ReadOnly {
		v.restorePending()
	}
	return v, nil
}

func (v *Volume) restorePending() {
	stages, err := openPending(v.staging)
	if err != nil {
		log.Printf("Read pending uploads: %v", err)
	}
	for _, p := range stages {
		key := fold(p.name)
		if v.files[key] != nil {
			log.Printf("Conflicting pending upload: %s", p.file.Name())
			p.file.Close()
			continue
		}
		p.attributes = windows.FILE_ATTRIBUTE_ARCHIVE
		v.files[key] = p
		for dir, virt := folder(p.name), p.dir; dir != ""; dir, virt = folder(dir), folder(virt) {
			v.dirs[fold(dir)] = Node{Path: dir, Virt: virt, Dir: true}
		}
	}
}

func (v *Volume) mount(letter string) (*wf.FileSystem, error) {
	attr := uint32(wf.FspFSAttributePostCleanupWhenModifiedOnly)
	if v.store.config.Filesystem.ReadOnly {
		attr |= wf.FspFSAttributeReadOnlyVolume
	}
	return wf.Mount(v, letter, wf.FileSystemName("-GooglePhotosFS"), wf.CasePreserveNames(true),
		wf.CreationTime(v.born), wf.SectorSize(512, 8), wf.FileInfoTimeout(250*time.Millisecond), wf.Attributes(attr))
}

func nativePath(name string) string { return strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/") }

func (v *Volume) findLocked(name string) (Node, *stage, error) {
	key := fold(name)
	if p := v.files[key]; p != nil {
		node := Node{Path: p.name, Virt: p.dir}
		return node, p, nil
	}
	node, err := v.store.node(name)
	if err == nil {
		return node, nil, nil
	}
	if dir, ok := v.dirs[key]; ok {
		return dir, nil, nil
	}
	return Node{}, nil, err
}
func (v *Volume) find(name string) (Node, *stage, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.findLocked(name)
}

func getHandle(v *Volume, id uintptr) (*fileHandle, error) {
	if h, ok := v.handles.Load(id); ok {
		return h.(*fileHandle), nil
	}
	return nil, windows.STATUS_INVALID_HANDLE
}

func (v *Volume) register(h *fileHandle) uintptr {
	id := uintptr(v.sequence.Add(1))
	v.handles.Store(id, h)
	return id
}

func filetime(t time.Time) uint64 {
	ticks := t.Unix() + 11644473600
	if ticks < 0 {
		return 0
	}
	return uint64(ticks)*1e7 + uint64(t.Nanosecond()/100)
}

func (v *Volume) describe(node Node, size int64, out *wf.FSP_FSCTL_FILE_INFO) {
	attrs := uint32(windows.FILE_ATTRIBUTE_ARCHIVE)
	created, modified := v.born, v.born
	if node.Dir {
		attrs = windows.FILE_ATTRIBUTE_DIRECTORY
		size = 0
	}
	if node.Media != nil {
		if node.Media.Taken > 0 {
			modified = time.UnixMilli(node.Media.Taken)
			created = modified
		}
		if node.Media.Uploaded > 0 {
			created = time.UnixMilli(node.Media.Uploaded)
		}
	}
	if size < 0 {
		size = 0
	}
	hash := fnv.New64a()
	key := fold(node.Path)
	if node.Media != nil {
		key = node.Media.Key
	}
	_, _ = hash.Write([]byte(key))
	*out = wf.FSP_FSCTL_FILE_INFO{
		FileAttributes: attrs, FileSize: uint64(size), AllocationSize: (uint64(size) + 4095) &^ 4095,
		CreationTime: filetime(created), LastAccessTime: filetime(modified), LastWriteTime: filetime(modified),
		ChangeTime: filetime(modified), IndexNumber: hash.Sum64(),
	}
}

func (v *Volume) describeStage(node Node, p *stage, out *wf.FSP_FSCTL_FILE_INFO) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, err := p.file.Stat()
	if err != nil {
		return err
	}
	v.describe(node, info.Size(), out)
	p.describe(out)
	return nil
}

func (v *Volume) describeOpen(h *fileHandle, out *wf.FSP_FSCTL_FILE_INFO) error {
	if h.pending != nil {
		return v.describeStage(h.node, h.pending, out)
	}
	var size int64
	if h.node.Media != nil {
		size = h.node.Media.Size
	}
	v.describe(h.node, size, out)
	return nil
}

func (p *stage) describe(out *wf.FSP_FSCTL_FILE_INFO) {
	out.FileAttributes = p.attributes
	if out.FileAttributes == 0 {
		out.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
	}
	if p.created != 0 {
		out.CreationTime = p.created
	}
	if p.accessed != 0 {
		out.LastAccessTime = p.accessed
	}
	if p.modified != 0 {
		out.LastWriteTime = p.modified
	}
	if p.changed != 0 {
		out.ChangeTime = p.changed
	}
}

func normalized(node Node, info *wf.FSP_FSCTL_FILE_INFO) {
	open := (*wf.FSP_FSCTL_OPEN_FILE_INFO)(unsafe.Pointer(info))
	name := utf16.Encode([]rune("\\" + strings.ReplaceAll(node.Path, "/", "\\")))
	if open.NormalizedName == nil || len(name)*2 > int(open.NormalizedNameSize) {
		return
	}
	copy(unsafe.Slice(open.NormalizedName, len(name)), name)
	open.NormalizedNameSize = uint16(len(name) * 2)
}

func callbackError(err error) error {
	if err == nil {
		return nil
	}
	var he HTTPError
	switch {
	case errors.Is(err, errReadOnly):
		return windows.STATUS_MEDIA_WRITE_PROTECTED
	case errors.Is(err, errVirtualDirectory):
		return windows.STATUS_CANNOT_DELETE
	case errors.Is(err, errNoDedup):
		return windows.STATUS_ACCESS_DENIED
	case errors.Is(err, context.DeadlineExceeded):
		return windows.STATUS_IO_TIMEOUT
	case errors.Is(err, context.Canceled):
		return windows.STATUS_CANCELLED
	case errors.As(err, &he):
		log.Print(err)
		switch he.Code {
		case 400:
			return windows.STATUS_INVALID_PARAMETER
		case 401, 403:
			return windows.STATUS_ACCESS_DENIED
		case 404:
			return windows.STATUS_FILE_NOT_AVAILABLE
		case 429:
			return windows.STATUS_IO_TIMEOUT
		case 507:
			return windows.STATUS_DISK_FULL
		default:
			return windows.STATUS_UNEXPECTED_NETWORK_ERROR
		}
	}
	return err
}

func (v *Volume) GetVolumeInfo(_ *wf.FileSystemRef, info *wf.FSP_FSCTL_VOLUME_INFO) error {
	v.usageMu.Lock()
	defer v.usageMu.Unlock()
	if time.Since(v.usageAt) > 30*time.Second {
		ctx, cancel := context.WithTimeout(v.ctx, 5*time.Second)
		u, err := v.store.photos.usage(ctx)
		cancel()
		if err == nil {
			v.usage = u
		} else {
			log.Print(err)
		}
		v.usageAt = time.Now()
	}
	*info = wf.FSP_FSCTL_VOLUME_INFO{}
	if v.usage.Total != nil {
		info.TotalSize = uint64(max(0, *v.usage.Total))
	}
	if v.usage.Free != nil && !v.store.config.Filesystem.ReadOnly {
		info.FreeSize = uint64(max(0, *v.usage.Free))
	}
	label := utf16.Encode([]rune("Google Photos"))
	info.VolumeLabelLength = uint16(len(label) * 2)
	copy(info.VolumeLabel[:], label)
	return nil
}

func (v *Volume) GetSecurityByName(_ *wf.FileSystemRef, name string, _ wf.GetSecurityByNameFlags) (uint32, *windows.SECURITY_DESCRIPTOR, error) {
	node, pending, err := v.find(nativePath(name))
	if err != nil {
		return 0, nil, err
	}
	if pending != nil {
		pending.mu.Lock()
		defer pending.mu.Unlock()
		info := wf.FSP_FSCTL_FILE_INFO{}
		pending.describe(&info)
		return info.FileAttributes, v.sd, nil
	}
	if node.Dir {
		return windows.FILE_ATTRIBUTE_DIRECTORY, v.sd, nil
	}
	return windows.FILE_ATTRIBUTE_ARCHIVE, v.sd, nil
}
func (v *Volume) GetSecurity(_ *wf.FileSystemRef, _ uintptr) (*windows.SECURITY_DESCRIPTOR, error) {
	return v.sd, nil
}

func (v *Volume) Open(_ *wf.FileSystemRef, name string, options, access uint32, info *wf.FSP_FSCTL_FILE_INFO) (uintptr, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	node, pending, err := v.findLocked(nativePath(name))
	if err != nil {
		return 0, err
	}
	if options&dirRequest != 0 && !node.Dir {
		return 0, windows.STATUS_NOT_A_DIRECTORY
	}
	if options&nonDirRequest != 0 && node.Dir {
		return 0, windows.STATUS_FILE_IS_A_DIRECTORY
	}
	write := access&dataAccess != 0 && !node.Dir
	if v.store.config.Filesystem.ReadOnly && (access&(dataAccess|windows.DELETE|windows.FILE_WRITE_ATTRIBUTES|windows.FILE_WRITE_EA|windows.WRITE_DAC|windows.WRITE_OWNER) != 0 || options&deleteOnClose != 0) {
		return 0, windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	if options&deleteOnClose != 0 && pending == nil {
		if node.Dir {
			return 0, windows.STATUS_CANNOT_DELETE
		}
		if node.Media == nil || node.Media.Dedup == "" {
			return 0, windows.STATUS_ACCESS_DENIED
		}
	}
	h := &fileHandle{node: node, pending: pending, writer: write}
	if err = v.describeOpen(h, info); err != nil {
		return 0, err
	}
	normalized(node, info)
	if pending != nil {
		pending.mu.Lock()
		if pending.committing && (write || options&deleteOnClose != 0) {
			pending.mu.Unlock()
			return 0, windows.STATUS_SHARING_VIOLATION
		}
		pending.refs++
		if write {
			pending.writers++
		}
		pending.mu.Unlock()
	}
	return v.register(h), nil
}

func (v *Volume) newStage(name, dir string) (*stage, error) {
	if err := os.MkdirAll(v.staging, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(v.staging, "upload-")
	if err != nil {
		return nil, err
	}
	return &stage{file: f, name: name, dir: dir, dirty: true, writers: 1, refs: 1, attributes: windows.FILE_ATTRIBUTE_ARCHIVE}, nil
}

func (v *Volume) Create(_ *wf.FileSystemRef, name string, options, access, attrs uint32, _ *windows.SECURITY_DESCRIPTOR,
	_ uint64, info *wf.FSP_FSCTL_FILE_INFO) (uintptr, error) {
	if v.store.config.Filesystem.ReadOnly {
		return 0, windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	name = nativePath(name)
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, _, err := v.findLocked(name); err == nil {
		return 0, os.ErrExist
	}
	parent, _, err := v.findLocked(folder(name))
	if err != nil || !parent.Dir {
		return 0, windows.STATUS_OBJECT_PATH_NOT_FOUND
	}
	node := Node{Path: path.Join(parent.Path, path.Base(name)), Dir: options&dirRequest != 0}
	h := &fileHandle{node: node}
	if node.Dir {
		node.Virt = path.Join(parent.Virt, path.Base(name))
		v.dirs[fold(node.Path)] = node
		h.node = node
	} else {
		node.Virt = parent.Virt
		h.node = node
		h.pending, err = v.newStage(node.Path, node.Virt)
		if err != nil {
			return 0, err
		}
		h.writer = true
		h.pending.attributes |= attrs & stagedAttributes
		v.files[fold(node.Path)] = h.pending
	}
	if err = v.describeOpen(h, info); err != nil {
		return 0, err
	}
	normalized(node, info)
	return v.register(h), nil
}

func (v *Volume) Overwrite(_ *wf.FileSystemRef, id uintptr, attrs uint32, replace bool, _ uint64, info *wf.FSP_FSCTL_FILE_INFO) error {
	if v.store.config.Filesystem.ReadOnly {
		return windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.node.Dir {
		return windows.STATUS_FILE_IS_A_DIRECTORY
	}
	if h.reader != nil {
		h.reader.Close()
		h.reader = nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.files[fold(h.node.Path)]
	if p == nil {
		p, err = v.newStage(h.node.Path, h.node.Virt)
		if err != nil {
			return err
		}
		v.files[fold(h.node.Path)] = p
	} else {
		p.mu.Lock()
		if p.committing {
			p.mu.Unlock()
			return windows.STATUS_SHARING_VIOLATION
		}
		err = p.unseal()
		if err == nil {
			err = p.file.Truncate(0)
		}
		if err == nil {
			p.dirty = true
			if h.pending != p {
				p.refs++
			}
			if h.pending != p || !h.writer {
				p.writers++
			}
		}
		p.mu.Unlock()
		if err != nil {
			return err
		}
	}
	p.mu.Lock()
	if replace {
		p.attributes = windows.FILE_ATTRIBUTE_ARCHIVE
	}
	p.attributes |= attrs & stagedAttributes
	p.mu.Unlock()
	h.pending, h.writer, h.node.Media = p, true, nil
	return v.describeOpen(h, info)
}

func (v *Volume) observeStage(h *fileHandle) {
	if h.pending != nil || h.node.Dir {
		return
	}
	v.mu.Lock()
	p := v.files[fold(h.node.Path)]
	if p != nil {
		p.mu.Lock()
		if !p.deleted && (!h.writer || !p.committing) {
			h.pending = p
			p.refs++
			if h.writer {
				p.writers++
			}
		}
		p.mu.Unlock()
	}
	v.mu.Unlock()
	if h.pending != nil && h.reader != nil {
		h.reader.Close()
		h.reader = nil
	}
}

func (v *Volume) GetFileInfo(_ *wf.FileSystemRef, id uintptr, info *wf.FSP_FSCTL_FILE_INFO) error {
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	v.observeStage(h)
	return v.describeOpen(h, info)
}

func (v *Volume) Read(_ *wf.FileSystemRef, id uintptr, p []byte, offset uint64) (int, error) {
	h, err := getHandle(v, id)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if offset > math.MaxInt64 {
		return 0, windows.STATUS_END_OF_FILE
	}
	if h.node.Dir {
		return 0, windows.STATUS_FILE_IS_A_DIRECTORY
	}
	v.observeStage(h)
	if h.pending != nil {
		h.pending.mu.Lock()
		defer h.pending.mu.Unlock()
		n, err := h.pending.file.ReadAt(p, int64(offset))
		if n > 0 && errors.Is(err, io.EOF) {
			err = nil
		}
		return n, err
	}
	if h.reader == nil {
		h.reader, err = v.store.downloads.open(*h.node.Media)
		if err != nil {
			return 0, callbackError(err)
		}
	}
	n, err := h.reader.ReadAt(p, int64(offset))
	if n > 0 && errors.Is(err, io.EOF) {
		err = nil
	}
	return n, callbackError(err)
}

// beginUpdate uses a local copy for Windows applications that write to an
// existing object. Google receives a complete upload when the writer closes.
func (v *Volume) beginUpdate(h *fileHandle, preserve bool) error {
	if h.node.Dir {
		return windows.STATUS_FILE_IS_A_DIRECTORY
	}
	if !h.writer {
		return windows.STATUS_ACCESS_DENIED
	}
	if h.pending != nil {
		return nil
	}
	useExisting := func() (bool, error) {
		p := v.files[fold(h.node.Path)]
		if p == nil {
			return false, nil
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.committing || p.deleted {
			return false, windows.STATUS_SHARING_VIOLATION
		}
		p.refs++
		p.writers++
		h.pending = p
		return true, nil
	}
	v.mu.Lock()
	found, err := useExisting()
	v.mu.Unlock()
	if found || err != nil {
		return err
	}
	p, err := v.newStage(h.node.Path, h.node.Virt)
	if err != nil {
		return err
	}
	p.dirty = false
	published := false
	defer func() {
		if !published {
			p.file.Close()
			os.Remove(p.file.Name())
		}
	}()
	if preserve {
		body, err := v.store.photos.download(v.ctx, h.node.Media.Key)
		if err != nil {
			return callbackError(err)
		}
		_, err = io.Copy(p.file, body)
		closeErr := body.Close()
		if err != nil || closeErr != nil {
			return callbackError(errors.Join(err, closeErr))
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if found, err := useExisting(); found || err != nil {
		return err
	}
	v.files[fold(p.name)] = p
	h.pending, published = p, true
	if h.reader != nil {
		h.reader.Close()
		h.reader = nil
	}
	return nil
}

func (v *Volume) Write(_ *wf.FileSystemRef, id uintptr, data []byte, offset uint64, appendFile, constrained bool,
	info *wf.FSP_FSCTL_FILE_INFO) (int, error) {
	if v.store.config.Filesystem.ReadOnly {
		return 0, windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	h, err := getHandle(v, id)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := v.beginUpdate(h, true); err != nil {
		return 0, err
	}
	p := h.pending
	p.mu.Lock()
	defer p.mu.Unlock()
	stat, err := p.file.Stat()
	if err != nil {
		return 0, err
	}
	size := stat.Size()
	if appendFile {
		offset = uint64(size)
	}
	if offset > math.MaxInt64 || uint64(len(data)) > math.MaxInt64-offset {
		return 0, windows.STATUS_FILE_TOO_LARGE
	}
	if constrained {
		if offset >= uint64(size) {
			v.describe(h.node, size, info)
			return 0, nil
		}
		data = data[:int(min(uint64(len(data)), uint64(size)-offset))]
	}
	if len(data) == 0 {
		v.describe(h.node, size, info)
		return 0, nil
	}
	if err := p.unseal(); err != nil {
		return 0, err
	}
	n, err := p.file.WriteAt(data, int64(offset))
	if n > 0 {
		p.dirty = true
	}
	v.describe(h.node, max(size, int64(offset)+int64(n)), info)
	p.describe(info)
	return n, err
}

func (v *Volume) SetFileSize(_ *wf.FileSystemRef, id uintptr, size uint64, allocation bool, info *wf.FSP_FSCTL_FILE_INFO) error {
	if v.store.config.Filesystem.ReadOnly {
		return windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if size > math.MaxInt64 {
		return windows.STATUS_FILE_TOO_LARGE
	}
	if h.pending == nil && !h.node.Dir && h.node.Media != nil && h.node.Media.Size >= 0 {
		current := uint64(h.node.Media.Size)
		if size == current || (allocation && size > current) {
			return v.describeOpen(h, info)
		}
	}
	if err := v.beginUpdate(h, size != 0); err != nil {
		return err
	}
	p := h.pending
	p.mu.Lock()
	defer p.mu.Unlock()
	stat, err := p.file.Stat()
	if err != nil {
		return err
	}
	actual := stat.Size()
	if int64(size) != actual && (!allocation || int64(size) < actual) || !allocation && size == 0 && !p.dirty {
		if err = p.unseal(); err != nil {
			return err
		}
		if err = p.file.Truncate(int64(size)); err != nil {
			return err
		}
		p.dirty = true
		actual = int64(size)
	}
	v.describe(h.node, actual, info)
	p.describe(info)
	return nil
}

func (v *Volume) SetBasicInfo(_ *wf.FileSystemRef, id uintptr, flags wf.SetBasicInfoFlags, attrs uint32,
	created, accessed, modified, changed uint64, info *wf.FSP_FSCTL_FILE_INFO) error {
	if v.store.config.Filesystem.ReadOnly {
		return windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if flags == 0 {
		return v.describeOpen(h, info)
	}
	p := h.pending
	if p == nil {
		return windows.STATUS_NOT_SUPPORTED
	}
	p.mu.Lock()
	if flags&wf.SetBasicInfoAttributes != 0 {
		p.attributes = attrs & stagedAttributes
	}
	if flags&wf.SetBasicInfoCreationTime != 0 && created != math.MaxUint64 {
		p.created = created
	}
	if flags&wf.SetBasicInfoLastAccessTime != 0 && accessed != math.MaxUint64 {
		p.accessed = accessed
	}
	if flags&wf.SetBasicInfoLastWriteTime != 0 && modified != math.MaxUint64 {
		p.modified = modified
	}
	if flags&wf.SetBasicInfoChangeTime != 0 && changed != math.MaxUint64 {
		p.changed = changed
	}
	p.mu.Unlock()
	return v.describeOpen(h, info)
}

func (v *Volume) commit(ctx context.Context, p *stage) error {
	p.mu.Lock()
	if p.deleted || !p.dirty || p.writers != 0 || p.committing {
		p.mu.Unlock()
		return nil
	}
	if err := p.seal(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.committing = true
	name, dir := p.name, p.dir
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	_, err := v.store.put(ctx, p.file, path.Base(name), dir)
	p.mu.Lock()
	p.committing = false
	if err == nil {
		err = p.unseal()
		p.dirty = err != nil
	}
	if err != nil {
		p.retryAt = time.Now().Add(30 * time.Second)
	}
	p.mu.Unlock()
	return err
}

// retire hides a completed stage. Existing handles keep its file until close.
func (v *Volume) retire(p *stage) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	key := fold(p.name)
	if v.files[key] != p || p.writers != 0 || p.committing || (p.dirty && !p.deleted) {
		return
	}
	delete(v.files, key)
	if p.refs == 0 {
		p.file.Close()
		os.Remove(p.file.Name())
	}
}

func (v *Volume) retryPending(ctx context.Context) {
	v.mu.Lock()
	stages := make([]*stage, 0, len(v.files))
	now := time.Now()
	for _, p := range v.files {
		p.mu.Lock()
		if p.sealed && p.dirty && !p.deleted && p.writers == 0 && !p.committing && !now.Before(p.retryAt) {
			stages = append(stages, p)
		}
		p.mu.Unlock()
	}
	v.mu.Unlock()
	var requests sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, p := range stages {
		select {
		case <-ctx.Done():
			requests.Wait()
			return
		case limit <- struct{}{}:
		}
		requests.Go(func() {
			defer func() { <-limit }()
			if err := v.commit(ctx, p); err != nil {
				if ctx.Err() == nil {
					log.Printf("Pending upload at %s: %v", p.file.Name(), err)
				}
				return
			}
			v.retire(p)
		})
	}
	requests.Wait()
}

func (v *Volume) startPending(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	var worker sync.WaitGroup
	worker.Go(func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for ctx.Err() == nil {
			v.retryPending(ctx)
			select {
			case <-ctx.Done():
				return
			case <-v.pendingWake:
			case <-tick.C:
			}
		}
	})
	return func() { cancel(); worker.Wait() }
}

func (v *Volume) Flush(_ *wf.FileSystemRef, id uintptr, info *wf.FSP_FSCTL_FILE_INFO) error {
	if id == 0 {
		return nil
	}
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending != nil && h.writer {
		// Flush makes the complete staged file durable locally. Google publishes
		// immutable media, so the last writer commits one logical transfer.
		h.pending.mu.Lock()
		err = h.pending.file.Sync()
		h.pending.mu.Unlock()
		if err != nil {
			return callbackError(err)
		}
	}
	return v.describeOpen(h, info)
}

func (v *Volume) CanDelete(_ *wf.FileSystemRef, id uintptr, _ string) error {
	if v.store.config.Filesystem.ReadOnly {
		return windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.node.Dir {
		v.mu.Lock()
		_, ephemeral := v.dirs[fold(h.node.Path)]
		v.mu.Unlock()
		if !ephemeral {
			return windows.STATUS_CANNOT_DELETE
		}
		if len(v.children(h.node)) != 0 {
			return windows.STATUS_DIRECTORY_NOT_EMPTY
		}
		return nil
	}
	if h.pending != nil {
		h.pending.mu.Lock()
		busy, readOnly := h.pending.committing, h.pending.attributes&windows.FILE_ATTRIBUTE_READONLY != 0
		h.pending.mu.Unlock()
		if readOnly {
			return windows.STATUS_CANNOT_DELETE
		}
		if busy {
			return windows.STATUS_SHARING_VIOLATION
		}
	}
	if h.pending == nil && h.node.Media.Dedup == "" {
		return windows.STATUS_ACCESS_DENIED
	}
	return nil
}

func (v *Volume) finish(h *fileHandle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cleaned {
		return
	}
	h.cleaned = true
	p := h.pending
	if p == nil || !h.writer {
		return
	}
	p.mu.Lock()
	p.writers--
	last := p.writers == 0
	needsUpload := last && p.dirty && !p.deleted
	var err error
	if needsUpload {
		err = p.seal()
	}
	p.mu.Unlock()
	if err != nil {
		log.Printf("Save pending upload %s: %v", p.file.Name(), err)
		return
	}
	if needsUpload {
		select {
		case v.pendingWake <- struct{}{}:
		default:
		}
	} else if last {
		v.retire(p)
	}
}

func (v *Volume) Cleanup(_ *wf.FileSystemRef, id uintptr, _ string, flags uint32) {
	if flags&wf.FspCleanupDelete == 0 {
		return
	}
	h, err := getHandle(v, id)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.pending; p != nil {
		p.mu.Lock()
		err := p.unseal()
		if err == nil {
			p.deleted = true
		}
		p.mu.Unlock()
		if err != nil {
			log.Printf("Cancel upload %s: %v", p.file.Name(), err)
			return
		}
		v.retire(p)
		return
	}
	if h.node.Dir {
		v.mu.Lock()
		delete(v.dirs, fold(h.node.Path))
		v.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(v.ctx, 2*time.Minute)
	defer cancel()
	if err := v.store.mutate(ctx, h.node, nil); err != nil {
		log.Printf("Delete %q: %v", h.node.Path, err)
	}
}

func (v *Volume) Close(_ *wf.FileSystemRef, id uintptr) {
	h, err := getHandle(v, id)
	if err != nil {
		return
	}
	v.finish(h)
	v.handles.Delete(id)
	if h.reader != nil {
		h.reader.Close()
	}
	h.dirBuf.Delete()
	if h.pending != nil {
		p := h.pending
		v.mu.Lock()
		p.mu.Lock()
		p.refs--
		if v.files[fold(p.name)] != p && p.refs == 0 {
			p.file.Close()
			if !p.dirty || p.deleted {
				os.Remove(p.file.Name())
			}
		}
		p.mu.Unlock()
		v.mu.Unlock()
	}
}

func (v *Volume) Rename(_ *wf.FileSystemRef, id uintptr, _, target string, replace bool) error {
	if v.store.config.Filesystem.ReadOnly {
		return windows.STATUS_MEDIA_WRITE_PROTECTED
	}
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil {
		return windows.STATUS_NOT_SUPPORTED
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	name := nativePath(target)
	parent, _, err := v.findLocked(folder(name))
	if err != nil || !parent.Dir {
		return windows.STATUS_OBJECT_PATH_NOT_FOUND
	}
	p := h.pending
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty || p.deleted || p.committing {
		return windows.STATUS_NOT_SUPPORTED
	}
	final := path.Join(parent.Path, path.Base(name))
	if other, otherStage, err := v.findLocked(final); err == nil && otherStage != p {
		if !replace || other.Dir || otherStage != nil {
			return os.ErrExist
		}
	}
	oldName, oldDir := p.name, p.dir
	p.name, p.dir = final, parent.Virt
	if p.sealed {
		p.sealed = false
		if err := p.seal(); err != nil {
			p.name, p.dir, p.sealed = oldName, oldDir, true
			return err
		}
	}
	delete(v.files, fold(oldName))
	h.node.Path, h.node.Virt = final, parent.Virt
	v.files[fold(final)] = p
	return nil
}

func (v *Volume) children(dir Node) []Node {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := fold(dir.Path)
	all := make(map[string]Node)
	if c := v.store.catalog.Load(); c != nil {
		for _, node := range c.children[key] {
			all[fold(node.Path)] = node
		}
	}
	for k, node := range v.dirs {
		if _, exists := all[k]; !exists && fold(folder(node.Path)) == key {
			all[k] = node
		}
	}
	for k, p := range v.files {
		if fold(folder(p.name)) == key {
			all[k] = Node{Path: p.name, Virt: p.dir}
		}
	}
	ordered := make([]Node, 0, len(all))
	for _, n := range all {
		ordered = append(ordered, n)
	}
	sort.Slice(ordered, func(i, j int) bool { return fold(ordered[i].Path) < fold(ordered[j].Path) })
	return ordered
}

func (v *Volume) GetOrNewDirBuffer(_ *wf.FileSystemRef, id uintptr) (*wf.DirBuffer, error) {
	h, err := getHandle(v, id)
	if err != nil {
		return nil, err
	}
	return &h.dirBuf, nil
}
func (v *Volume) ReadDirectory(_ *wf.FileSystemRef, id uintptr, _ string, fill func(string, *wf.FSP_FSCTL_FILE_INFO) (bool, error)) error {
	h, err := getHandle(v, id)
	if err != nil {
		return err
	}
	if !h.node.Dir {
		return windows.STATUS_NOT_A_DIRECTORY
	}
	dir := h.node
	var info wf.FSP_FSCTL_FILE_INFO
	if dir.Path != "" {
		v.describe(dir, 0, &info)
		if ok, err := fill(".", &info); !ok || err != nil {
			return err
		}
		if parent, _, err := v.find(folder(dir.Path)); err == nil {
			v.describe(parent, 0, &info)
		}
		if ok, err := fill("..", &info); !ok || err != nil {
			return err
		}
	}
	for _, node := range v.children(dir) {
		var size int64
		if node.Media != nil {
			size = node.Media.Size
		}
		v.describe(node, size, &info)
		if !node.Dir && node.Media == nil {
			if _, p, err := v.find(node.Path); err == nil && p != nil {
				_ = v.describeStage(node, p, &info)
			}
		}
		if ok, err := fill(path.Base(node.Path), &info); !ok || err != nil {
			return err
		}
	}
	return nil
}

func (v *Volume) shutdown() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	var pending []string
	for _, p := range v.files {
		p.mu.Lock()
		if p.dirty && !p.deleted && p.writers == 0 {
			if err := p.seal(); err != nil {
				log.Printf("Save pending upload %s: %v", p.file.Name(), err)
			}
		}
		p.file.Close()
		if !p.dirty || p.deleted {
			os.Remove(p.file.Name())
		} else {
			pending = append(pending, p.file.Name())
		}
		p.mu.Unlock()
	}
	if len(pending) != 0 {
		return fmt.Errorf("pending uploads retained in %s", strings.Join(pending, ", "))
	}
	return nil
}
