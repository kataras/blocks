package blocks

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
)

func getFS(fsOrDir any) fs.FS {
	switch v := fsOrDir.(type) {
	case string:
		return os.DirFS(v)
	case fs.FS:
		return v
	default:
		panic(fmt.Errorf(`blocks: unexpected "fsOrDir" argument type of %T (want a directory name, an fs.FS, an embed.FS or a *MemoryFileSystem)`, v))
	}
}

// readFiles reads every regular file in fsys and returns the contents keyed by slash
// separated path. Leading and trailing whitespace is trimmed from each file, so a
// template that begins with a newline does not render one.
//
// It reads in the order WalkDir yields, stops at the first error, and returns the
// context's error if ctx is done. Reading in parallel was measured and did not pay: the
// file systems this package is given are memory, embed or a small directory on disk, and
// the goroutine per file cost more than the reads it overlapped.
//
// Only regular files are read. A symlink is neither regular nor a directory, so it is
// skipped, which is what stops a link inside the template directory from reaching a file
// outside it.
func readFiles(ctx context.Context, fsys fs.FS) (map[string][]byte, error) {
	files := make(map[string][]byte)

	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}

		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}

		files[name] = bytes.TrimSpace(data)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return files, ctx.Err()
}
