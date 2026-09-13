# Changelog

All notable changes to this project are recorded here. Versions follow
[semantic versioning](https://semver.org), and dates are ISO 8601.

## v0.1.0 - 2026-09-13

The Go 1.27 release, and the first one that breaks anything. A leading zero in the version
says the API is not yet stable, and going to 0.1.0 rather than 0.0.13 is the part that says
so out loud.

### Breaking

**The minimum Go version is 1.27.** The `go` directive moved from 1.26 to 1.27. A module
that depends on this one has to declare 1.27 or later itself.

**The `Root`, `Templates` and `Layouts` fields are gone.** They were the engine's mutable
state, read without a lock while `Load` refilled them. Their replacements are read-only and
safe to call at any time:

| Before | Now |
| --- | --- |
| `views.Templates["index"]` | `views.Lookup("index", "")` |
| `views.Layouts["main"+"index"]` | `views.Lookup("index", "main")` |
| `for name := range views.Templates` | `for name := range views.TemplateNames()` |
| `for name := range views.Layouts` | `for name := range views.LayoutNames()` |
| `views.Root.Funcs(m)` | `views.Funcs(m)` |
| `views.Root.Option(o)` | `views.Option(o)` |

`TemplateNames` and `LayoutNames` return an `iter.Seq[string]` in sorted order, so a listing
no longer changes between runs.

**go-bindata is no longer supported, and neither is `http.FileSystem`.** `New` takes a
directory name, an `fs.FS` (an `embed.FS` included) or a `*MemoryFileSystem`. Use `embed`,
which the standard library has had since Go 1.16:

```go
//go:embed views
var embedded embed.FS

views := blocks.New(embedded).RootDir("views")
```

The `_examples/embedded-bindata` program goes with it. That support was already broken: see
the nested directory fix below.

**`ErrNotExist` carries three fields instead of one,** `Name`, `Layout` and `IsLayout`, and
its message is prefixed with the package name. Code that builds one by hand needs updating.
Code that checks for one does not, and has a better option now: `ErrNotExist` matches
`fs.ErrNotExist`, so `errors.Is(err, fs.ErrNotExist)` works.

**A bare `{{ yield }}` passes the data.** It used to pass nil, so a layout written that way
rendered the page with no data at all and printed nothing where its fields should be. A
layout that relied on the old behavior renders differently now.

**`{{- yield . -}}` trims the whitespace around it.** The trim markers were dropped when the
action was rewritten, so they did nothing. Layouts using them lose the newline and the
indentation they were written to remove.

**`Option` reaches layouts.** With `missingkey=error`, a layout that reads a missing key now
returns an error where it used to render an empty value.

**A file whose function name merely starts with `yield`, such as `{{ yielding }}`, is a page
rather than a layout.** The detection matched any action beginning with those five letters.

### Security

**A parse error no longer carries the template source.** `Load` used to append the whole
file to the error message, and the handler everyone writes passes `err.Error()` to the
reader. The new `*LoadError` names the file and wraps the parse error, and nothing in the
package puts file contents into an error. See `TestLoadErrorDoesNotCarryTemplateSource`.

**`Delims` rejects a delimiter that is not valid UTF-8.** A delimiter becomes part of the
expressions that read each file, and an invalid one used to panic from inside `Load`, far
from the call that caused it. Found by `FuzzReplaceYield`.

### Fixed

**Reload mode could crash the process.** `Load` cleared and refilled the template maps in
place while other goroutines read them, and the Go runtime aborts on a concurrent map read
and write. Templates are published as an immutable set now, swapped in one step, so a render
in flight keeps the set it started with. `TestReloadIsSafeUnderConcurrentRenders` covers it
under the race detector.

**A failed `Load` left the engine half-loaded.** It cleared the templates first and parsed
second, so a syntax error in one file took out every template that had not been reparsed
yet. A failed load changes nothing now and returns the error.

**`AddFunc` and `Register` wrote shared maps with no lock.** iris calls `AddFunc` from inside
a request handler, so this was reachable from ordinary use. `Funcs` and `LayoutFuncs` also
kept the caller's map by reference and then wrote into it.

**Layout keys collided.** The key was the two names concatenated, so layout `ab` with page
`c` and layout `a` with page `bc` were one entry, and one of the two rendered the wrong page.

**`MemoryFileSystem` was rewritten.** It crashed with a stack overflow on a name beginning
with a slash, handed every reader of a file the same value with its offset reset, and failed
`testing/fstest.TestFS` on sorting, names, modification times and directory reads. It passes
`TestFS` now, copies the contents it is given, validates names, and implements
`fs.ReadFileFS` and `fs.StatFS`.

**Nested directories were lost over an `http.FileSystem`.** Subdirectory entries were
flattened into their parent under the wrong names, so `Load` failed on any layout in a
subdirectory. That is why the go-bindata example did not work. The adapter is gone.

**`RootDir` panicked on three of the four ways to write a path.** `/views`, `./views` and
`views/` all panicked, and the bindata example documented the first of them. All four are
accepted now.

**A layout could not be named with its directory.** `ExecuteTemplate(w, "index",
"layouts/main", data)` failed even though that is the file's actual path.

**`ErrNotExist` named the wrong thing.** Rendering a page that does not exist, under a layout
that does, reported the layout as missing.

**A layout that calls a function from `Funcs` failed to load.** Layouts were parsed before
the engine's function map was applied, so only `LayoutFuncs` worked in a layout.

**A broken layout went unnoticed when there were no pages.** Layouts were parsed only inside
the loop over pages, so with nothing to fill them a syntax error was never seen.

**A page written as `{{- define "content" }}` was wrapped in a second define** and failed to
parse. The check for an existing define did not understand trim markers.

**Custom delimiters broke layout detection.** The expressions that classify files had `{{`
and `}}` written into them, so with `Delims("[[", "]]")` a layout was read as a page.

**`LoadWithContext` leaked goroutines on cancel.** It returned while the readers it started
were still running. Reading is sequential now, and checks the context per file.

**`Register` called after `New` reached layouts but not pages.** The two took their copy of
the registered functions at different moments. Both take it at `Load` now.

### Added

- `Lookup(tmplName, layoutName)`, `TemplateNames()` and `LayoutNames()`.
- `ErrNoTemplates`, a sentinel for a file system with nothing to load.
- `LoadError`, with the file name and the wrapped parse error.
- `FuncMapper`, the interface a file system implements to carry template functions.
- `MemoryFileSystem.TemplateFuncs`, `ReadFile` and `Stat`.

### Changed

**The function map passed to `MemoryFileSystem.ParseTemplate` works.** It was stored and
never read. Those functions are added to the engine at the next `Load`, and a value that is
not a usable function is refused by `ParseTemplate` rather than ignored.

**Markdown is rendered by [goldmark](https://github.com/yuin/goldmark) instead of
blackfriday,** which has had no release since 2020. The configuration keeps what was there
(tables, strikethrough, bare links, definition lists, smart quotes, XHTML void elements) and
adds task lists and heading identifiers. Raw HTML still passes through, which needs
goldmark's unsafe renderer option: without it an HTML block becomes a comment, and this
package strips comments, so the markup would disappear with nothing said. Output differs
from blackfriday in the whitespace between blocks and in a few entity choices.

**`valyala/bytebufferpool` was dropped** for a `sync.Pool` of standard buffers. goldmark is
now the only dependency.

**Each layout is parsed once and cloned per page,** rather than reparsed for every page. A
load of 50 pages and one layout went from 12,614 allocations and 964 KB to 11,242 and
850 KB. Render allocations are unchanged. Wall-clock timings on the development machine were
too noisy to quote.

**Compiled expressions are cached per delimiter pair,** which matters in reload mode where
every request loads.

**Go 1.27 modernizers were applied** with `go fix`, and the package uses `iter.Seq`,
`slices.Sorted`, `maps.Clone`, `atomic.Pointer` and the `clear` builtin.

### Documentation

- A `doc.go` overview, and runnable examples for embedding, directories, memory templates, HTTP handlers, `Lookup` and `Set`.
- The README was rewritten around `embed`. [DOC.md](DOC.md) is a guide to the parts that come up later: errors, concurrency, functions, markdown, testing and the sharp edges.
- Every exported symbol has a doc comment that matches what the code does. Several did not.

### Tests and tooling

- Golden tests capture the rendered output of layouts, blocks, yield forms, partials, markdown, comment stripping, escaping and the name rules. They were written against the old code first, so every behavior change in this release was seen as a golden diff and kept on purpose.
- Fuzz targets cover comment removal, the yield rewrite, content wrapping and layout detection. One of them found the delimiter panic listed above.
- CI runs on Go 1.27 with vet, the race detector, a gofmt check, `go fix -diff`, golangci-lint and govulncheck, and builds every example. The old examples script started servers that never exited, under `continue-on-error`, so nothing was ever really checked.

### Upgrading

Set `go 1.27` in your own `go.mod`. Then:

1. Replace reads of `Templates`, `Layouts` and `Root` with `Lookup`, `TemplateNames`, `LayoutNames`, `Funcs` and `Option`, per the table above.
2. If you passed a go-bindata `AssetFile()` or an `http.FileSystem` to `New`, switch to `embed`.
3. Look at any layout using a bare `{{ yield }}` or trim markers around a yield, and at any engine setting `missingkey`. Those three render differently now.
4. Markdown output shifts slightly. If you compare rendered HTML in tests, regenerate those fixtures and read the diff.
