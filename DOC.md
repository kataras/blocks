# Blocks in depth

The [README](README.md) is enough to render a page. This guide covers the parts that come up later: how a file becomes a template, what the engine does with errors, what is safe to call concurrently, and where the sharp edges are.

## How a file becomes a template

Every file under the root directory goes through the same four steps at `Load`.

1. **Read.** Only regular files are read. A symlink is skipped, which is what keeps a link inside the template directory from pulling in a file outside it. Leading and trailing whitespace is trimmed, so a file that starts with a blank line does not render one.
2. **Convert.** A file whose extension has a registered parser is converted first. One parser ships by default, `.md`. A file whose extension is neither the engine's own (`.html`) nor a registered one is ignored.
3. **Classify.** A file that contains `{{ template "content" ... }}` or `{{ yield ... }}` is a layout. Everything else is a page. HTML comments are removed before this decision, because the template parser reads a `define` inside a comment as a real one, and because a commented-out `yield` would otherwise turn a page into a layout.
4. **Parse.** A page that defines no template of its own is wrapped in `{{ define "content" }}...{{ end }}`. Each layout is parsed once and then cloned for every page, so L layouts and P pages produce L times P templates that each know about one page.

That last step is why any page renders under any layout, and why `Load` costs more than a plain `template.ParseGlob`. Loading 50 pages and one layout takes about 11,000 allocations, so do it at startup rather than per request.

## Rendering

```go
err := views.ExecuteTemplate(w, "index", "main", data)
```

The third argument is the layout. Pass an empty string to render the page on its own, or to fall back to `DefaultLayout`.

`TemplateString` returns the result instead of writing it. `PartialFunc` renders a page's content block and returns `template.HTML`, which is what the `partial` function calls.

`Lookup` reaches the parsed template, for the cases those three do not cover, such as rendering one named block:

```go
tmpl := views.Lookup("partials/footer", "")
err := tmpl.ExecuteTemplate(w, "content", data)
```

What `Lookup` returns is the live template, shared with every other render. Read it and execute it. Do not call `Parse` or `Funcs` on it, and do not hold it across a reload: the next `Load` replaces it.

## Errors

| Error | When | What to do |
| --- | --- | --- |
| `blocks.ErrNoTemplates` | `Load` found no usable file | Check `RootDir` and `Extension` |
| `*blocks.LoadError` | One file failed to parse | `err.File` names it, `errors.Unwrap` gives the parse error |
| `blocks.ErrNotExist` | A name did not match anything loaded | Check the name, then the layout directory |

`ErrNotExist` matches `fs.ErrNotExist`, so a handler can treat a missing template as a 404:

```go
if errors.Is(err, fs.ErrNotExist) {
	http.NotFound(w, r)
	return
}
```

It also carries which part is missing:

```go
if notExist, ok := errors.AsType[blocks.ErrNotExist](err); ok {
	log.Printf("template %q, layout %q, layout missing: %t",
		notExist.Name, notExist.Layout, notExist.IsLayout)
}
```

A `LoadError` never carries the file's contents, only its name. That matters because the obvious handler writes `err.Error()` somewhere, and the source of a template is not something to publish. Log the error, and send the reader a status code.

## Concurrency

Configure the engine, call `Load`, then serve. After that:

- `ExecuteTemplate`, `TemplateString`, `PartialFunc`, `Lookup`, `TemplateNames` and `LayoutNames` are safe from any number of goroutines.
- `Load` is safe to call again at any time. It builds a new set of templates and publishes it in one step, so a render in flight keeps the set it started with.
- A failed `Load` changes nothing. The engine keeps serving the templates from the last successful one and returns the error.
- The setters (`Funcs`, `AddFunc`, `Option` and the rest) are safe to call at any time, but they take effect at the next `Load`. Templates already parsed keep the functions they were parsed with.

With `Reload(true)`, every render reparses first. Concurrent renders collapse into one reparse rather than each doing their own.

## Functions

Three places to put a function, weakest first:

1. `blocks.Register` adds it to every engine in the program. Call it from an `init`.
2. `LayoutFuncs` adds it to layouts only.
3. `Funcs` and `AddFunc` add it to pages and layouts, and override the two above.

A function map value is usually a plain function. Two other forms are accepted by `Register`, for a function that needs the engine it belongs to:

```go
blocks.Register(template.FuncMap{
	"render": func(v *blocks.Blocks) any { return v.PartialFunc },
})
```

A value that html/template will not accept panics at the call that passed it, rather than failing later inside `Load`.

## Markdown

The `.md` parser is goldmark with GFM (tables, strikethrough, task lists, bare links), definition lists, the typographer, heading identifiers written as `{#id}`, XHTML void elements, and raw HTML passed through.

Raw HTML matters here. Without it goldmark replaces an HTML block with a comment, and this package strips comments, so the markup would disappear with nothing said.

Two things to know:

- **A markdown file is a template after conversion.** `{{` inside a code block is still an action. A fenced block showing template syntax needs escaping.
- **A markdown file keeps its extension in its name.** `notes.md` is the template `notes.md`, because `notes.html` could exist beside it.

To use a different markdown library, or to convert another extension:

```go
views.Extensions(".md", func(contents []byte) ([]byte, error) {
	return myMarkdown(contents)
})
```

Passing `nil` drops the extension, after which those files are ignored.

## Delimiters and options

```go
views := blocks.New("./views").Delims("[[", "]]").Option("missingkey=error")
```

Layout detection follows the delimiters, so `[[ yield . ]]` is recognized as a layout. Options reach every template, layouts included.

A delimiter has to be valid UTF-8, because it becomes part of the expressions that read each file. One that is not panics at the `Delims` call.

## Building templates at runtime

```go
mfs := blocks.NewMemoryFileSystem()
if err := mfs.ParseTemplate("index.html", contents, funcs); err != nil {
	return err
}
```

`MemoryFileSystem` is a complete `fs.FS`: it passes `testing/fstest.TestFS`, implements `ReadFile` and `Stat`, sorts directory entries, and is safe for concurrent use. Names are normalized, so a leading slash or a backslash is accepted, and a name that escapes the file system is refused.

The contents are copied, so the caller may reuse the slice. The function map is optional, and when given, those functions are added to the engine at the next `Load`.

Anything implementing `blocks.FuncMapper` carries functions the same way, so a file system of your own can do it too:

```go
type FuncMapper interface {
	TemplateFuncs() template.FuncMap
}
```

## Testing your templates

`MemoryFileSystem` keeps a template test to one file, with no fixtures on disk:

```go
func TestGreeting(t *testing.T) {
	mfs := blocks.NewMemoryFileSystem()
	if err := mfs.ParseTemplate("index.html", []byte(`<p>{{ .Name }}</p>`), nil); err != nil {
		t.Fatal(err)
	}

	views := blocks.New(mfs)
	if err := views.Load(); err != nil {
		t.Fatal(err)
	}

	got, err := views.TemplateString("index", "", map[string]any{"Name": "World"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "<p>World</p>" {
		t.Errorf("got %q", got)
	}
}
```

For real template files, point the engine at `testdata` or embed them, and compare against a golden file. That is how this package tests itself: see `golden_test.go`.

## Sharp edges

- **A page that defines anything must define `content`.** The wrapper is only added to a page with no `define` of its own. A page that defines a `title` block and then writes markup at the top level replaces the layout body with that markup, because the last parse of a template body wins in html/template.
- **Comments are removed everywhere**, including inside a `<script>` or `<pre>` block. A JavaScript string holding `<!--` is affected.
- **Template names are not file paths.** They use forward slashes on every platform, and never carry the root or the layout directory.
- **`AddFunc` after `Load` does nothing until the next `Load`.** The templates are already parsed.

## Migrating from an older version

The `Root`, `Templates` and `Layouts` fields are gone. `Lookup(name, layout)` replaces a map read, and `TemplateNames` and `LayoutNames` iterate what is loaded:

```go
for name := range views.TemplateNames() {
	log.Println(name, views.Lookup(name, ""))
}
```

go-bindata is no longer supported. Use `embed`, which the standard library has had since Go 1.16:

```go
//go:embed views
var embedded embed.FS

views := blocks.New(embedded).RootDir("views")
```

[CHANGELOG.md](CHANGELOG.md) lists every change with what it breaks.
