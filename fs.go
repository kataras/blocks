package blocks

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

func getFS(fsOrDir any) fs.FS {
	switch v := fsOrDir.(type) {
	case string:
		return os.DirFS(v)
	case http.FileSystem: // handles go-bindata.
		return &httpFS{v}
	case fs.FS:
		return v
	default:
		panic(fmt.Errorf(`blocks: unexpected "fsOrDir" argument type of %T (string or fs.FS or embed.FS or http.FileSystem)`, v))
	}
}

// walk recursively in "fileSystem" descends "root" path, calling "walkFn".
func walk(fileSystem fs.FS, root string, walkFn filepath.WalkFunc) error {
	if root != "" && root != "/" {
		sub, err := fs.Sub(fileSystem, root)
		if err != nil {
			return err
		}

		fileSystem = sub
	}

	if root == "" {
		root = "."
	}

	return fs.WalkDir(fileSystem, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		info, err := d.Info()
		if err != nil {
			if err != filepath.SkipDir {
				return fmt.Errorf("%s: %w", path, err)
			}

			return nil
		}

		if info.IsDir() {
			return nil
		}

		return walkFn(path, info, err)
	})

}

func asset(fileSystem fs.FS, name string) ([]byte, error) {
	return fs.ReadFile(fileSystem, name)
}

type httpFS struct {
	fs http.FileSystem
}

func (f *httpFS) Open(name string) (fs.File, error) {
	if name == "." {
		name = "/"
	}

	return f.fs.Open(filepath.ToSlash(name))
}

func (f *httpFS) ReadDir(name string) ([]fs.DirEntry, error) {
	name = filepath.ToSlash(name)
	if name == "." {
		name = "/"
	}

	file, err := f.fs.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	infos, err := file.Readdir(-1)
	if err != nil {
		return nil, err
	}

	entries := make([]fs.DirEntry, 0, len(infos))
	for _, info := range infos {
		if info.IsDir() { // http file's does not return the whole tree, so read it.
			sub, err := f.ReadDir(info.Name())
			if err != nil {
				return nil, err
			}

			entries = append(entries, sub...)
			continue
		}

		entry := fs.FileInfoToDirEntry(info)
		entries = append(entries, entry)
	}

	return entries, nil
}

// readFiles reads all files from an fs.FS concurrently and returns a map[string][]byte.
func readFiles(ctx context.Context, fsys fs.FS, root string) (map[string][]byte, error) {
	if root != "" && root != "/" {
		sub, err := fs.Sub(fsys, root)
		if err != nil {
			return nil, err
		}

		fsys = sub
	}

	if root == "" {
		root = "."
	}

	files := make(map[string][]byte)

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		errChan = make(chan error, 1)
	)

	// Walk the file system
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		info, err := d.Info()
		if err != nil {
			if err != filepath.SkipDir {
				return fmt.Errorf("%s: %w", path, err)
			}

			return nil
		}

		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				select {
				case errChan <- err:
				default:
				}
				return
			}

			select {
			case <-ctx.Done():
				return
			default:
			}

			// trim top and bottom space.
			data = bytes.TrimLeftFunc(data, unicode.IsSpace)
			data = bytes.TrimRightFunc(data, unicode.IsSpace)

			mu.Lock()
			files[path] = data
			mu.Unlock()
		}(path)

		return nil
	})

	if err != nil {
		return nil, err
	}

	// Wait for all goroutines to finish
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Check for errors
	select {
	case err := <-errChan:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return files, nil
}

// MemoryFileSystem is a custom file system that holds virtual/memory template files in memory.
// It completes the fs.FS interface.
type MemoryFileSystem struct {
	files map[string]*memoryTemplateFile
}

// NewMemoryFileSystem creates a new VirtualFileSystem instance.
// It comes with no files, use `ParseTemplate` to add new virtual/memory template files.
// Usage:
//
//	mfs := NewVirtualFileSystem()
//	err := mfs.ParseTemplate("example.html", []byte("Hello, World!"), nil)
//	templates := New(mfs)
//	templates.Load()
func NewMemoryFileSystem() *MemoryFileSystem {
	return &MemoryFileSystem{
		files: make(map[string]*memoryTemplateFile),
	}
}

// Ensure MemoryFileSystem implements fs.FS, fs.ReadDirFS and fs.WalkDirFS interfaces.
var (
	_ fs.FS        = (*MemoryFileSystem)(nil)
	_ fs.ReadDirFS = (*MemoryFileSystem)(nil)
)

// ParseTemplate adds a new memory temlate to the file system.
func (vfs *MemoryFileSystem) ParseTemplate(name string, contents []byte, funcMap template.FuncMap) error {
	vfs.files[name] = &memoryTemplateFile{
		name:     name,
		contents: contents,
		funcMap:  funcMap,
	}
	return nil
}

// Open implements the fs.FS interface.
func (mfs *MemoryFileSystem) Open(name string) (fs.File, error) {
	if name == "." || name == "/" {
		// Return a directory representing the root.
		return &memoryDir{
			fs:   mfs,
			name: ".",
		}, nil
	}

	if mfs.isDir(name) {
		// Return a directory.
		return &memoryDir{
			fs:   mfs,
			name: name,
		}, nil
	}

	if file, exists := mfs.files[name]; exists {
		file.reset() // Reset read position
		return file, nil
	}

	return nil, fs.ErrNotExist
}

// ReadDir implements the fs.ReadDirFS interface.
func (mfs *MemoryFileSystem) ReadDir(name string) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	prefix := strings.TrimLeftFunc(name, func(r rune) bool {
		return r == '.' || r == '/'
	})
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	seen := make(map[string]bool)

	for path := range mfs.files {
		if !strings.HasPrefix(path, prefix) {
			continue
		}

		trimmedPath := strings.TrimPrefix(path, prefix)
		parts := strings.SplitN(trimmedPath, "/", 2)
		entryName := parts[0]

		if seen[entryName] {
			continue
		}
		seen[entryName] = true

		fullPath := prefix + entryName
		if mfs.isDir(fullPath) {
			info := &memoryDirInfo{name: entryName}
			entries = append(entries, fs.FileInfoToDirEntry(info))
		} else {
			file, _ := mfs.files[fullPath]
			info := &memoryFileInfo{
				name: entryName,
				size: int64(len(file.contents)),
			}
			entries = append(entries, fs.FileInfoToDirEntry(info))
		}
	}

	return entries, nil
}

// isDir checks if the given name is a directory in the memory file system.
func (mfs *MemoryFileSystem) isDir(name string) bool {
	dirPrefix := name
	if dirPrefix != "" && !strings.HasSuffix(dirPrefix, "/") {
		dirPrefix += "/"
	}
	for path := range mfs.files {
		if strings.HasPrefix(path, dirPrefix) {
			return true
		}
	}
	return false
}

type memoryDir struct {
	fs      *MemoryFileSystem
	name    string
	offset  int
	entries []fs.DirEntry
}

// Ensure memoryDir implements fs.ReadDirFile interface.
var _ fs.ReadDirFile = (*memoryDir)(nil)

// Read implements the io.Reader interface.
func (d *memoryDir) Read(p []byte) (int, error) {
	return 0, io.EOF // Directories cannot be read as files.
}

// Close implements the io.Closer interface.
func (d *memoryDir) Close() error {
	return nil
}

// Stat implements the fs.File interface.
func (d *memoryDir) Stat() (fs.FileInfo, error) {
	return &memoryDirInfo{
		name: d.name,
	}, nil
}

// ReadDir implements the fs.ReadDirFile interface.
func (d *memoryDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.entries == nil {
		// Initialize the entries slice.
		entries, err := d.fs.ReadDir(d.name)
		if err != nil {
			return nil, err
		}
		d.entries = entries
	}

	if d.offset >= len(d.entries) {
		return nil, io.EOF
	}

	if n <= 0 || d.offset+n > len(d.entries) {
		n = len(d.entries) - d.offset
	}

	entries := d.entries[d.offset : d.offset+n]
	d.offset += n

	return entries, nil
}

// memoryDirInfo provides directory information for a memory directory.
type memoryDirInfo struct {
	name string
}

// Ensure memoryDirInfo implements fs.FileInfo interface.
var _ fs.FileInfo = (*memoryDirInfo)(nil)

// Name returns the base name of the directory.
func (di *memoryDirInfo) Name() string {
	return di.name
}

// Size returns the length in bytes (zero for directories).
func (di *memoryDirInfo) Size() int64 {
	return 0
}

// Mode returns file mode bits.
func (di *memoryDirInfo) Mode() fs.FileMode {
	return fs.ModeDir | 0555 // Readable directory
}

// ModTime returns modification time.
func (di *memoryDirInfo) ModTime() time.Time {
	return time.Now()
}

// IsDir reports if the file is a directory.
func (di *memoryDirInfo) IsDir() bool {
	return true
}

// Sys returns underlying data source (can return nil).
func (di *memoryDirInfo) Sys() interface{} {
	return nil
}

// memoryTemplateFile represents a memory file.
type memoryTemplateFile struct {
	name     string
	contents []byte
	funcMap  template.FuncMap
	offset   int64
}

// Ensure memoryTemplateFile implements fs.File interface.
var _ fs.File = (*memoryTemplateFile)(nil)

func (mf *memoryTemplateFile) reset() {
	mf.offset = 0
}

// Stat implements the fs.File interface, returning file info.
func (mf *memoryTemplateFile) Stat() (fs.FileInfo, error) {
	return &memoryFileInfo{
		name: path.Base(mf.name),
		size: int64(len(mf.contents)),
	}, nil
}

// Read implements the io.Reader interface.
func (mf *memoryTemplateFile) Read(p []byte) (int, error) {
	if mf.offset >= int64(len(mf.contents)) {
		return 0, io.EOF
	}
	n := copy(p, mf.contents[mf.offset:])
	mf.offset += int64(n)
	return n, nil
}

// Close implements the io.Closer interface.
func (mf *memoryTemplateFile) Close() error {
	return nil
}

// memoryFileInfo provides file information for a memory file.
type memoryFileInfo struct {
	name string
	size int64
}

// Ensure memoryFileInfo implements fs.FileInfo interface.
var _ fs.FileInfo = (*memoryFileInfo)(nil)

// Name returns the base name of the file.
func (fi *memoryFileInfo) Name() string {
	return fi.name
}

// Size returns the length in bytes for regular files.
func (fi *memoryFileInfo) Size() int64 {
	return fi.size
}

// Mode returns file mode bits.
func (fi *memoryFileInfo) Mode() fs.FileMode {
	return 0444 // Read-only
}

// ModTime returns modification time.
func (fi *memoryFileInfo) ModTime() time.Time {
	return time.Now()
}

// IsDir reports if the file is a directory.
func (fi *memoryFileInfo) IsDir() bool {
	return false
}

// Sys returns underlying data source (can return nil).
func (fi *memoryFileInfo) Sys() interface{} {
	return nil
}
