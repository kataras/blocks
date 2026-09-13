package blocks

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryFileSystem holds template files in memory. It implements fs.FS, so it can be
// passed to New like any other file system, and it is safe for concurrent use.
//
// Use it when the templates are built at runtime, or when a test would rather not write
// files to disk.
//
//	mfs := blocks.NewMemoryFileSystem()
//	err := mfs.ParseTemplate("index.html", []byte("<h1>Hello, {{ .Name }}!</h1>"), nil)
//	views := blocks.New(mfs)
//	err = views.Load()
type MemoryFileSystem struct {
	mu    sync.RWMutex
	files map[string]*memoryFile
	dirs  map[string]struct{} // every directory implied by a file name, including ".".
	funcs template.FuncMap    // the union of the function maps given to ParseTemplate.
}

// NewMemoryFileSystem returns an empty file system. Add files to it with ParseTemplate.
func NewMemoryFileSystem() *MemoryFileSystem {
	return &MemoryFileSystem{
		files: make(map[string]*memoryFile),
		dirs:  map[string]struct{}{".": {}},
	}
}

var (
	_ fs.FS         = (*MemoryFileSystem)(nil)
	_ fs.ReadDirFS  = (*MemoryFileSystem)(nil)
	_ fs.ReadFileFS = (*MemoryFileSystem)(nil)
	_ fs.StatFS     = (*MemoryFileSystem)(nil)
)

// memoryFile is one stored file. Its contents are never handed out by reference.
type memoryFile struct {
	name     string
	contents []byte
}

// ParseTemplate stores a template file under the given name, which is a slash separated
// path such as "layouts/main.html". A name may be given with a leading slash or with
// backslashes, and both are normalized. It returns an error if the name escapes the file
// system, or if a value in funcMap is not a function usable by html/template.
//
// The contents are copied, so the caller may reuse the slice.
//
// The functions in funcMap are added to the engine's function map the next time the
// engine loads, which makes them available to every template rather than to this one
// alone. A nil funcMap adds nothing. Calling ParseTemplate again with the same name
// replaces the file.
func (mfs *MemoryFileSystem) ParseTemplate(name string, contents []byte, funcMap template.FuncMap) error {
	cleaned, err := cleanMemoryName(name)
	if err != nil {
		return err
	}

	if err := validateFuncMap(funcMap); err != nil {
		return fmt.Errorf("blocks: %s: %w", name, err)
	}

	mfs.mu.Lock()
	defer mfs.mu.Unlock()

	mfs.files[cleaned] = &memoryFile{name: cleaned, contents: bytes.Clone(contents)}
	for dir := path.Dir(cleaned); ; dir = path.Dir(dir) {
		mfs.dirs[dir] = struct{}{}
		if dir == "." {
			break
		}
	}

	if len(funcMap) > 0 {
		if mfs.funcs == nil {
			mfs.funcs = make(template.FuncMap, len(funcMap))
		}
		maps.Copy(mfs.funcs, funcMap)
	}

	return nil
}

// cleanMemoryName turns a caller's file name into a path that fs.ValidPath accepts.
func cleanMemoryName(name string) (string, error) {
	cleaned := path.Clean(strings.TrimLeft(filepath.ToSlash(name), "/"))
	if cleaned == "." || !fs.ValidPath(cleaned) {
		return "", &fs.PathError{Op: "parsetemplate", Path: name, Err: fs.ErrInvalid}
	}
	return cleaned, nil
}

// validateFuncMap reports whether every value is a function that html/template accepts.
// Template.Funcs panics on a bad value, so the panic becomes an error here rather than
// surfacing much later, out of a Load the caller did not expect to fail.
func validateFuncMap(funcMap template.FuncMap) (err error) {
	if len(funcMap) == 0 {
		return nil
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid function map: %v", r)
		}
	}()

	template.New("blocks.funcmap.check").Funcs(funcMap)
	return nil
}

// TemplateFuncs returns the functions collected from every ParseTemplate call, or nil if
// there are none. An engine calls it at Load.
func (mfs *MemoryFileSystem) TemplateFuncs() template.FuncMap {
	mfs.mu.RLock()
	defer mfs.mu.RUnlock()

	if len(mfs.funcs) == 0 {
		return nil
	}
	return maps.Clone(mfs.funcs)
}

// Open implements fs.FS. Each call returns an independent reader, so one file can be read
// by many goroutines at once.
func (mfs *MemoryFileSystem) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	mfs.mu.RLock()
	defer mfs.mu.RUnlock()

	if f, ok := mfs.files[name]; ok {
		return &memoryOpenFile{info: fileInfoOf(f), reader: bytes.NewReader(f.contents)}, nil
	}

	if _, ok := mfs.dirs[name]; ok {
		entries, err := mfs.readDir(name)
		if err != nil {
			return nil, err
		}
		return &memoryOpenDir{info: dirInfoOf(name), entries: entries}, nil
	}

	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadDir implements fs.ReadDirFS. The entries are sorted by file name.
func (mfs *MemoryFileSystem) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}

	mfs.mu.RLock()
	defer mfs.mu.RUnlock()

	return mfs.readDir(name)
}

// readDir lists the direct children of a directory. Callers hold at least a read lock.
func (mfs *MemoryFileSystem) readDir(name string) ([]fs.DirEntry, error) {
	if _, ok := mfs.dirs[name]; !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}

	var entries []fs.DirEntry
	for child, f := range mfs.files {
		if path.Dir(child) == name {
			entries = append(entries, fs.FileInfoToDirEntry(fileInfoOf(f)))
		}
	}
	for child := range mfs.dirs {
		if child != "." && path.Dir(child) == name {
			entries = append(entries, fs.FileInfoToDirEntry(dirInfoOf(child)))
		}
	}

	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// ReadFile implements fs.ReadFileFS. The returned slice is a copy.
func (mfs *MemoryFileSystem) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrInvalid}
	}

	mfs.mu.RLock()
	defer mfs.mu.RUnlock()

	f, ok := mfs.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
	}
	return bytes.Clone(f.contents), nil
}

// Stat implements fs.StatFS.
func (mfs *MemoryFileSystem) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}

	mfs.mu.RLock()
	defer mfs.mu.RUnlock()

	if f, ok := mfs.files[name]; ok {
		return fileInfoOf(f), nil
	}
	if _, ok := mfs.dirs[name]; ok {
		return dirInfoOf(name), nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// memoryInfo describes a file or a directory. The modification time is the zero time:
// nothing in memory has one, and reading a clock here would make two calls about the same
// file disagree.
type memoryInfo struct {
	name  string
	size  int64
	mode  fs.FileMode
	isDir bool
}

var _ fs.FileInfo = memoryInfo{}

func fileInfoOf(f *memoryFile) memoryInfo {
	return memoryInfo{name: path.Base(f.name), size: int64(len(f.contents)), mode: 0o444}
}

func dirInfoOf(name string) memoryInfo {
	return memoryInfo{name: path.Base(name), mode: fs.ModeDir | 0o555, isDir: true}
}

func (i memoryInfo) Name() string       { return i.name }
func (i memoryInfo) Size() int64        { return i.size }
func (i memoryInfo) Mode() fs.FileMode  { return i.mode }
func (i memoryInfo) ModTime() time.Time { return time.Time{} }
func (i memoryInfo) IsDir() bool        { return i.isDir }
func (i memoryInfo) Sys() any           { return nil }

// memoryOpenFile is one reader over one file's contents.
type memoryOpenFile struct {
	info   memoryInfo
	reader *bytes.Reader
}

var (
	_ fs.File     = (*memoryOpenFile)(nil)
	_ io.Seeker   = (*memoryOpenFile)(nil)
	_ io.ReaderAt = (*memoryOpenFile)(nil)
)

func (f *memoryOpenFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memoryOpenFile) Read(p []byte) (int, error) { return f.reader.Read(p) }
func (f *memoryOpenFile) Close() error               { return nil }

func (f *memoryOpenFile) Seek(offset int64, whence int) (int64, error) {
	return f.reader.Seek(offset, whence)
}

func (f *memoryOpenFile) ReadAt(p []byte, off int64) (int, error) {
	return f.reader.ReadAt(p, off)
}

// memoryOpenDir is one open directory, holding the listing taken when it was opened.
type memoryOpenDir struct {
	info    memoryInfo
	entries []fs.DirEntry
	offset  int
}

var _ fs.ReadDirFile = (*memoryOpenDir)(nil)

func (d *memoryOpenDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *memoryOpenDir) Close() error               { return nil }

func (d *memoryOpenDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: fs.ErrInvalid}
}

// ReadDir implements fs.ReadDirFile. With n at or below zero it returns everything left
// and a nil error, which is what fs.ReadDir asks for. With a positive n it pages, and
// reports io.EOF once there is nothing left.
func (d *memoryOpenDir) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := d.entries[d.offset:]

	if n <= 0 {
		d.offset = len(d.entries)
		return slices.Clone(remaining), nil
	}

	if len(remaining) == 0 {
		return nil, io.EOF
	}

	if n > len(remaining) {
		n = len(remaining)
	}
	d.offset += n
	return slices.Clone(remaining[:n]), nil
}
