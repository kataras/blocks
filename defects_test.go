package blocks_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/kataras/blocks"
)

// This file holds one test per known defect, each asserting the behavior the package
// should have. A test that is still skipped names the task group that fixes it. Remove
// the skip in that group and not before: a test that passes only because it never ran is
// worse than no test at all.

func memoryFS(t *testing.T, files map[string]string) *blocks.MemoryFileSystem {
	t.Helper()

	mfs := blocks.NewMemoryFileSystem()
	for name, contents := range files {
		if err := mfs.ParseTemplate(name, []byte(contents), nil); err != nil {
			t.Fatalf("parse template %s: %v", name, err)
		}
	}
	return mfs
}

func loaded(t *testing.T, files map[string]string) *blocks.Blocks {
	t.Helper()

	v := blocks.New(memoryFS(t, files))
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return v
}

func mustExecute(t *testing.T, v *blocks.Blocks, tmpl, layout string, data any) string {
	t.Helper()

	var buf bytes.Buffer
	if err := v.ExecuteTemplate(&buf, tmpl, layout, data); err != nil {
		t.Fatalf("execute %s with layout %q: %v", tmpl, layout, err)
	}
	return buf.String()
}

// diskFS writes the files into a temporary directory and returns that directory.
func diskFS(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, contents := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Group 3: MemoryFileSystem.

func TestMemoryFileSystemPassesTestFS(t *testing.T) {
	mfs := memoryFS(t, map[string]string{
		"index.html":           "<p>index</p>",
		"layouts/main.html":    `{{ template "content" . }}`,
		"partials/footer.html": "<small>footer</small>",
	})

	if err := fstest.TestFS(mfs, "index.html", "layouts/main.html", "partials/footer.html"); err != nil {
		t.Error(err)
	}
}

func TestMemoryFileSystemLeadingSlashName(t *testing.T) {
	// A leading slash used to produce a directory entry named "", which sent WalkDir
	// around the root until the stack overflowed.
	v := blocks.New(memoryFS(t, map[string]string{
		"/index.html":        "<p>index</p>",
		"/layouts/main.html": `<body>{{ template "content" . }}</body>`,
	}))
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, want := mustExecute(t, v, "index", "main", nil), "<body><p>index</p></body>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMemoryFileSystemConcurrentReads(t *testing.T) {
	// Every Open used to hand back the same value with its read offset reset, so two
	// readers of one file corrupted each other. Run with -race.
	mfs := memoryFS(t, map[string]string{"index.html": strings.Repeat("<p>index</p>", 500)})

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				f, err := mfs.Open("index.html")
				if err != nil {
					t.Error(err)
					return
				}
				b, err := io.ReadAll(f)
				f.Close()
				if err != nil {
					t.Error(err)
					return
				}
				if len(b) != 500*len("<p>index</p>") {
					t.Errorf("short read: %d bytes", len(b))
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestMemoryFileSystemCopiesContents(t *testing.T) {
	contents := []byte("<p>original</p>")
	mfs := blocks.NewMemoryFileSystem()
	if err := mfs.ParseTemplate("index.html", contents, nil); err != nil {
		t.Fatal(err)
	}
	copy(contents, []byte("<p>mutated!</p>"))

	got, err := fs.ReadFile(mfs, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<p>original</p>" {
		t.Errorf("the file system kept the caller's slice: got %q", got)
	}
}

func TestMemoryFileSystemRejectsInvalidName(t *testing.T) {
	mfs := blocks.NewMemoryFileSystem()
	if err := mfs.ParseTemplate("../escape.html", []byte("x"), nil); err == nil {
		t.Error("expected an error for a name that escapes the file system")
	}
}

// Group 4: RootDir.

func TestRootDirAcceptsAnySpelling(t *testing.T) {
	files := map[string]string{
		"views/index.html":        "<p>index</p>",
		"views/layouts/main.html": `<body>{{ template "content" . }}</body>`,
	}

	// RootDir used to panic on every spelling but the bare one, and the bindata example
	// recommends the "/views" form.
	for _, root := range []string{"views", "/views", "./views", "views/"} {
		t.Run(root, func(t *testing.T) {
			v := blocks.New(memoryFS(t, files)).RootDir(root)
			if err := v.Load(); err != nil {
				t.Fatalf("load: %v", err)
			}
			if got, want := mustExecute(t, v, "index", "main", nil), "<body><p>index</p></body>"; got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

// Group 5: reading files.

func TestLoadWithContextStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	v := blocks.New(memoryFS(t, map[string]string{"index.html": "<p>index</p>"}))
	if err := v.LoadWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestSymlinkedTemplatesAreSkipped(t *testing.T) {
	// Only regular files are read, which is what keeps a symlink from reaching outside
	// the template directory. This locks that in rather than fixing anything.
	dir := diskFS(t, map[string]string{
		"index.html":        "<p>index</p>",
		"layouts/main.html": `<body>{{ template "content" . }}</body>`,
	})
	secret := filepath.Join(t.TempDir(), "secret.html")
	if err := os.WriteFile(secret, []byte("<p>secret</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "leak.html")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	v := blocks.New(dir)
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	var buf bytes.Buffer
	if err := v.ExecuteTemplate(&buf, "leak", "", nil); err == nil {
		t.Errorf("a symlinked template was loaded: %q", buf.String())
	}
}

// Group 6: the snapshot, and the maps shared with callers.

func TestReloadIsSafeUnderConcurrentRenders(t *testing.T) {
	// Reload(true) reparses on every request while other requests are rendering. The
	// maps used to be cleared and refilled in place, which the runtime aborts on. Run
	// with -race.
	mfs := memoryFS(t, map[string]string{
		"index.html":           "<p>{{ .Name }}</p>",
		"partials/footer.html": "<small>footer</small>",
		"layouts/main.html":    `<body>{{ template "content" . }}{{ partial "partials/footer" . }}</body>`,
	})

	v := blocks.New(mfs).Reload(true)
	if err := v.Load(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				var buf bytes.Buffer
				if err := v.ExecuteTemplate(&buf, "index", "main", map[string]any{"Name": "go"}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 50 {
			v.AddFunc("noop", func() string { return "" })
			_ = mfs.ParseTemplate("extra.html", fmt.Appendf(nil, "<p>%d</p>", i), nil)
		}
	})
	wg.Wait()
}

func TestFailedLoadKeepsTheWorkingTemplates(t *testing.T) {
	mfs := memoryFS(t, map[string]string{
		"index.html":        "<p>index</p>",
		"layouts/main.html": `<body>{{ template "content" . }}</body>`,
	})
	v := blocks.New(mfs)
	if err := v.Load(); err != nil {
		t.Fatal(err)
	}
	want := mustExecute(t, v, "index", "main", nil)

	// Break a template, then reload. The engine keeps serving what it had.
	if err := mfs.ParseTemplate("layouts/main.html", []byte(`{{ template "content" . }}{{ if }}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := v.Load(); err == nil {
		t.Fatal("expected a parse error")
	}

	if got := mustExecute(t, v, "index", "main", nil); got != want {
		t.Errorf("after a failed Load the engine served %q, want %q", got, want)
	}
}

func TestFuncsDoesNotKeepTheCallersMap(t *testing.T) {
	callers := template.FuncMap{"first": func() string { return "1" }}

	v := blocks.New(memoryFS(t, map[string]string{"index.html": "<p>{{ first }}</p>"}))
	v.Funcs(callers)
	v.Funcs(template.FuncMap{"second": func() string { return "2" }})
	v.AddFunc("third", func() string { return "3" })

	if _, ok := callers["second"]; ok {
		t.Error("Funcs wrote into the caller's map")
	}
	if _, ok := callers["third"]; ok {
		t.Error("AddFunc wrote into the caller's map")
	}
}

func TestRegisterIsSafeWithConcurrentLoads(t *testing.T) {
	// Register writes a package-level map that every Load reads. Run with -race.
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 50 {
			blocks.Register(template.FuncMap{
				fmt.Sprintf("registered%d", i): func() string { return "" },
			})
		}
	})
	wg.Go(func() {
		for range 50 {
			v := blocks.New(memoryFS(t, map[string]string{"index.html": "<p>index</p>"}))
			if err := v.Load(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
}

// Group 7: layout keys, name normalization and errors.

func TestLayoutKeysDoNotCollide(t *testing.T) {
	// The key was layoutName+tmplName, so layout "ab" with content "c" and layout "a"
	// with content "bc" both became "abc".
	v := loaded(t, map[string]string{
		"layouts/a.html":  `[a]{{ template "content" . }}`,
		"layouts/ab.html": `[ab]{{ template "content" . }}`,
		"bc.html":         "<p>bc</p>",
		"c.html":          "<p>c</p>",
	})

	if got, want := mustExecute(t, v, "bc", "a", nil), "[a]<p>bc</p>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := mustExecute(t, v, "c", "ab", nil), "[ab]<p>c</p>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLayoutNameAcceptsItsDirectory(t *testing.T) {
	v := loaded(t, map[string]string{
		"index.html":        "<p>index</p>",
		"layouts/main.html": `<body>{{ template "content" . }}</body>`,
	})

	want := "<body><p>index</p></body>"
	for _, layout := range []string{"main", "main.html", "layouts/main", "layouts/main.html"} {
		if got := mustExecute(t, v, "index", layout, nil); got != want {
			t.Errorf("layout %q: got %q, want %q", layout, got, want)
		}
	}
}

func TestErrNotExistNamesWhatIsMissing(t *testing.T) {
	v := loaded(t, map[string]string{
		"index.html":        "<p>index</p>",
		"layouts/main.html": `<body>{{ template "content" . }}</body>`,
	})

	err := v.ExecuteTemplate(io.Discard, "nope", "main", nil)
	notExist, ok := errors.AsType[blocks.ErrNotExist](err)
	if !ok {
		t.Fatalf("got %v, want blocks.ErrNotExist", err)
	}
	if notExist.Name != "nope" {
		t.Errorf("the error names %q, but the missing template is %q", notExist.Name, "nope")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Error("ErrNotExist should match fs.ErrNotExist")
	}
}

func TestLoadErrorDoesNotCarryTemplateSource(t *testing.T) {
	// The whole file used to be appended to the error, and the README hands err.Error()
	// to the client.
	v := blocks.New(memoryFS(t, map[string]string{
		"secret.html": "API_KEY=SECRET-TOKEN-b3a9 {{ if }}",
	}))

	err := v.Load()
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("the error carries the template source: %v", err)
	}
	if !strings.Contains(err.Error(), "secret.html") {
		t.Errorf("the error should name the file: %v", err)
	}
}

func TestBrokenLayoutFailsWithoutContentTemplates(t *testing.T) {
	// Layouts were only parsed inside the content loop, so with no content template a
	// broken layout loaded cleanly.
	v := blocks.New(memoryFS(t, map[string]string{
		"layouts/main.html": `{{ template "content" . }}{{ if }}`,
	}))

	if err := v.Load(); err == nil {
		t.Error("expected a parse error for the broken layout")
	}
}

// Group 8: delimiters, options and yield.

func TestOptionReachesLayouts(t *testing.T) {
	v := blocks.New(memoryFS(t, map[string]string{
		"index.html":        "<p>index</p>",
		"layouts/main.html": `<body>{{ template "content" . }}{{ .Missing }}</body>`,
	}))
	v.Option("missingkey=error")
	if err := v.Load(); err != nil {
		t.Fatal(err)
	}

	if err := v.ExecuteTemplate(io.Discard, "index", "main", map[string]any{}); err == nil {
		t.Error("missingkey=error did not reach the layout")
	}
}

func TestCustomDelimitersDetectLayouts(t *testing.T) {
	v := blocks.New(memoryFS(t, map[string]string{
		"index.html":        "<p>[[ .Name ]]</p>",
		"layouts/main.html": "<body>[[ yield . ]]</body>",
	}))
	v.Delims("[[", "]]")
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	want := "<body><p>go</p></body>"
	if got := mustExecute(t, v, "index", "main", map[string]any{"Name": "go"}); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTrimMarkerDefineIsNotWrapped(t *testing.T) {
	// A content file that already defines "content", written with a trim marker, used to
	// be wrapped in a second define and fail to parse.
	v := blocks.New(memoryFS(t, map[string]string{
		"index.html":        `{{- define "content" }}<p>index</p>{{ end }}`,
		"layouts/main.html": `<body>{{ template "content" . }}</body>`,
	}))
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, want := mustExecute(t, v, "index", "main", nil), "<body><p>index</p></body>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBareYieldPassesTheData(t *testing.T) {
	v := loaded(t, map[string]string{
		"index.html":        "<p>{{ .Name }}</p>",
		"layouts/main.html": "<body>{{ yield }}</body>",
	})

	if got, want := mustExecute(t, v, "index", "main", map[string]any{"Name": "go"}), "<body><p>go</p></body>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestYieldKeepsItsTrimMarkers(t *testing.T) {
	v := loaded(t, map[string]string{
		"index.html":        "<p>index</p>",
		"layouts/main.html": "<body>\n\t{{- yield . -}}\n</body>",
	})

	if got, want := mustExecute(t, v, "index", "main", nil), "<body><p>index</p></body>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLayoutDetectionDoesNotMatchOtherFunctions(t *testing.T) {
	// A template that calls a function whose name merely starts with "yield" is content,
	// not a layout.
	v := blocks.New(memoryFS(t, map[string]string{
		"index.html":        "<p>{{ yielding }}</p>",
		"layouts/main.html": `<body>{{ template "content" . }}</body>`,
	}))
	v.Funcs(template.FuncMap{"yielding": func() string { return "crop" }})
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, want := mustExecute(t, v, "index", "main", nil), "<body><p>crop</p></body>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLayoutCanCallFuncs(t *testing.T) {
	// Layouts are parsed before the engine's own function map is applied, so a layout
	// calling a Funcs function fails to load.
	v := blocks.New(memoryFS(t, map[string]string{
		"index.html":        "<p>index</p>",
		"layouts/main.html": `<body>{{ template "content" . }} {{ year }}</body>`,
	}))
	v.Funcs(template.FuncMap{"year": func() int { return 2026 }})

	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := mustExecute(t, v, "index", "main", nil), "<body><p>index</p> 2026</body>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Group 9: functions carried by the file system.

func TestMemoryFileSystemFuncMapIsUsed(t *testing.T) {
	mfs := blocks.NewMemoryFileSystem()
	if err := mfs.ParseTemplate("index.html", []byte("<p>{{ greet .Name }}</p>"),
		template.FuncMap{"greet": func(s string) string { return "Hello, " + s }}); err != nil {
		t.Fatal(err)
	}

	v := blocks.New(mfs)
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := mustExecute(t, v, "index", "", map[string]any{"Name": "go"}), "<p>Hello, go</p>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseTemplateRejectsAnInvalidFunc(t *testing.T) {
	mfs := blocks.NewMemoryFileSystem()
	err := mfs.ParseTemplate("index.html", []byte("x"), template.FuncMap{"notAFunc": 42})
	if err == nil {
		t.Error("expected an error for a function map value that is not a function")
	}
}

// Group 6, continued: the replacements for the exported Templates and Layouts maps.

func TestLookupAndNames(t *testing.T) {
	v := loaded(t, map[string]string{
		"index.html":        "<p>index</p>",
		"about.html":        "<p>about</p>",
		"layouts/main.html": `<body>{{ template "content" . }}</body>`,
	})

	if got := slices.Collect(v.TemplateNames()); !slices.Equal(got, []string{"about", "index"}) {
		t.Errorf("template names: %v", got)
	}
	if got := slices.Collect(v.LayoutNames()); !slices.Equal(got, []string{"main"}) {
		t.Errorf("layout names: %v", got)
	}
	if v.Lookup("index", "") == nil {
		t.Error("Lookup returned nil for a template that exists")
	}
	if v.Lookup("index.html", "layouts/main.html") == nil {
		t.Error("Lookup did not normalize the names the way ExecuteTemplate does")
	}
	if v.Lookup("nope", "") != nil {
		t.Error("Lookup returned a template for a name that does not exist")
	}
}

func TestLookupBeforeLoad(t *testing.T) {
	v := blocks.New(memoryFS(t, map[string]string{"index.html": "<p>index</p>"}))

	if v.Lookup("index", "") != nil {
		t.Error("Lookup returned a template before Load")
	}
	if len(slices.Collect(v.TemplateNames())) != 0 {
		t.Error("TemplateNames returned names before Load")
	}
	if err := v.ExecuteTemplate(io.Discard, "index", "", nil); err == nil {
		t.Error("ExecuteTemplate before Load should fail")
	}
}

func TestNoTemplatesIsASentinel(t *testing.T) {
	v := blocks.New(blocks.NewMemoryFileSystem())
	if err := v.Load(); !errors.Is(err, blocks.ErrNoTemplates) {
		t.Errorf("got %v, want blocks.ErrNoTemplates", err)
	}
}

func TestDelimsRejectsInvalidUTF8(t *testing.T) {
	// Found by FuzzReplaceYield. The delimiters go into the expressions that read each
	// file, so an invalid rune used to panic from inside Load, far from the call that
	// caused it.
	defer func() {
		if recover() == nil {
			t.Error("expected a panic for a delimiter that is not valid UTF-8")
		}
	}()

	blocks.New(memoryFS(t, map[string]string{"index.html": "x"})).Delims(string([]byte{0xe8}), "}}")
}
