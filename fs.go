package blocks

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
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

// MemoryFileSystem is a custom file system that holds virtual/memory template files in memory.
// It completes the fs.FS interface.
type MemoryFileSystem struct {
	files map[string]*memoryTemplateFile
}

// NewMemoryFileSystem creates a new VirtualFileSystem instance.
// It comes with no files, use `ParseTemplate` to add new virtual/memory template files.
// Usage:
//
//	vfs := NewVirtualFileSystem()
//	err := vfs.ParseTemplate("example.html", []byte("Hello, World!"), nil)
//	templates := New(vfs)
//	templates.Load()
func NewMemoryFileSystem() *MemoryFileSystem {
	return &MemoryFileSystem{
		files: make(map[string]*memoryTemplateFile),
	}
}

var _ fs.FS = (*MemoryFileSystem)(nil)

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
func (vfs *MemoryFileSystem) Open(name string) (fs.File, error) {
	if file, exists := vfs.files[name]; exists {
		file.reset() // Reset read position
		return file, nil
	}
	return nil, fs.ErrNotExist
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

func (vf *memoryTemplateFile) reset() {
	vf.offset = 0
}

// Stat implements the fs.File interface, returning file info.
func (vf *memoryTemplateFile) Stat() (fs.FileInfo, error) {
	return &memoryFileInfo{
		name: vf.name,
		size: int64(len(vf.contents)),
	}, nil
}

// Read implements the io.Reader interface.
func (vf *memoryTemplateFile) Read(p []byte) (int, error) {
	if vf.offset >= int64(len(vf.contents)) {
		return 0, io.EOF
	}
	n := copy(p, vf.contents[vf.offset:])
	vf.offset += int64(n)
	return n, nil
}

// Close implements the io.Closer interface.
func (vf *memoryTemplateFile) Close() error {
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
