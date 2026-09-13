# Blocks

[![build status](https://img.shields.io/github/actions/workflow/status/kataras/blocks/ci.yml?style=for-the-badge)](https://github.com/kataras/blocks/actions) [![report card](https://img.shields.io/badge/report%20card-a%2B-ff3333.svg?style=for-the-badge)](https://goreportcard.com/report/github.com/kataras/blocks) [![godocs](https://img.shields.io/badge/go-%20docs-488AC7.svg?style=for-the-badge)](https://pkg.go.dev/github.com/kataras/blocks)

Blocks is a view engine for Go, built on [html/template](https://pkg.go.dev/html/template). You write a layout once, write each page as a plain file, and render any page inside any layout.

- Layouts, blocks and partials
- Templates from a directory, an [embed.FS](https://pkg.go.dev/embed#FS), any [fs.FS](https://pkg.go.dev/io/fs#FS), or memory
- Markdown pages, through [goldmark](https://github.com/yuin/goldmark)
- Reload while you edit, safely, with requests in flight
- Template functions per engine, per layout, or shared across every engine
- HTML comments stripped before parsing
- Loads under a context, for a slow file system

Requires Go 1.27 or later.

## Installation

```sh
go get github.com/kataras/blocks@latest
```

## Getting started

Create a **./views** directory:

```
│   main.go
└───views
    │   index.html
    ├───layouts
    │       main.html
    └───partials
            footer.html
```

**./views/layouts/main.html** is the frame every page renders inside:

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>{{ if .Title }}{{ .Title }}{{ else }}Default Title{{ end }}</title>
</head>
<body>
    {{ template "content" . }}

    <footer>{{ partial "partials/footer" . }}</footer>
</body>
</html>
```

`template "content"` is where the page goes. `{{ yield . }}` is shorthand for the same thing. `partial` renders another template in place, and the dot passes the current data to it.

**./views/index.html** is a page. It needs no wrapper:

```html
<h1>Index Body</h1>
```

**./main.go** loads the directory once and renders from a handler:

```go
package main

import (
	"log"
	"net/http"

	"github.com/kataras/blocks"
)

var views = blocks.New("./views")

func main() {
	if err := views.Load(); err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/", index)

	log.Println("Listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]any{"Title": "Index Title"}
	if err := views.ExecuteTemplate(w, "index", "main", data); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		log.Println(err)
	}
}
```

Load parses every file. Do it once at startup, before serving. An engine is safe to use from many goroutines after that.

Send the error text to your log, not to the browser. A parse error names the file that failed.

## Embedding the templates

`embed` puts the templates inside the binary, so the program has no directory to ship:

```go
import "embed"

//go:embed views
var embedded embed.FS

var views = blocks.New(embedded).RootDir("views")
```

`RootDir` picks the directory inside the file system and is trimmed from every name, so the page above stays `index` rather than `views/index`. Any `fs.FS` works the same way, including what `fs.Sub` returns.

## Names

A template is named by its path under the root, without the extension.

| File | Name |
| --- | --- |
| `views/index.html` | `index` |
| `views/admin/users.html` | `admin/users` |
| `views/layouts/main.html` | `main`, the layout |
| `views/notes.md` | `notes.md` |

The extension is optional when you render: `index` and `index.html` are the same page. A layout may be named with or without its directory, so `main`, `main.html` and `layouts/main.html` all work. Markdown keeps its extension in the name, because `notes.md` and `notes.html` are two different files.

## Blocks

A layout can leave a hole with a default in it, and a page can replace it:

```html
<!-- layouts/main.html -->
<title>{{ block "title" . }}Default Title{{ end }}</title>
```

```html
<!-- index.html -->
{{ define "title" }}Index Title{{ end }}
{{ define "content" }}<h1>Index Body</h1>{{ end }}
```

A page that defines nothing keeps the layout defaults. Note the shape of that second file: once a page defines anything, it has to define `content` too, because the wrapper is only added to a page that defines nothing.

## Template functions

```go
views := blocks.New("./views").
	Funcs(template.FuncMap{
		"year": func() int { return time.Now().Year() },
	})
```

`Funcs` covers pages and layouts. `LayoutFuncs` adds functions only layouts can call. `blocks.Register` adds functions to every engine in the program, which is how a package of shared helpers ships:

```go
package myfuncs

func init() {
	blocks.Register(template.FuncMap{"markdownify": markdownify})
}
```

Functions are read when `Load` runs. Add them first.

## Markdown

A `.md` file is rendered to HTML and then parsed as a template, so a markdown page can use actions and render inside a layout. Tables, strikethrough, task lists, definition lists, bare links and raw HTML blocks all pass through.

One sharp edge: the file is a template after the markdown is converted, so `{{` inside a code block is still a template action. Escape it, or call `Extensions(".md", yourParser)` with a parser of your own.

## Reloading while you edit

```go
views := blocks.New("./views").Reload(true)
```

Every render reparses the directory. A reload builds a new set of templates and swaps it in, so a request already rendering finishes on the set it started with, and a file with a syntax error leaves the last working templates in place and returns the error. Turn it off in production: it reparses every file on every request.

## Options

All of these return the engine, and all of them come before `Load`.

| Method | What it does |
| --- | --- |
| `RootDir(dir)` | The directory inside the file system that holds the templates |
| `LayoutDir(dir)` | Where layouts live, `layouts` by default |
| `DefaultLayout(name)` | The layout used when you pass an empty one |
| `Extension(ext)` | The template extension, `.html` by default |
| `Extensions(ext, parser)` | A parser for another extension, or nil to ignore it |
| `Delims(left, right)` | The action delimiters, `{{` and `}}` by default |
| `Option(opts...)` | The html/template options, such as `missingkey=error` |
| `Funcs`, `LayoutFuncs`, `AddFunc` | Template functions |
| `Reload(bool)` | Reparse on every render |

## Templates built at runtime

`MemoryFileSystem` holds templates with no files on disk, which suits generated pages and tests:

```go
mfs := blocks.NewMemoryFileSystem()
mfs.ParseTemplate("layouts/main.html", []byte(`<main>{{ yield . }}</main>`), nil)
mfs.ParseTemplate("index.html", []byte(`<p>{{ greet .Name }}</p>`),
	template.FuncMap{"greet": func(s string) string { return "Hello, " + s }})

views := blocks.New(mfs)
err := views.Load()
```

The function map is optional. When you pass one, those functions are added to the engine at the next `Load`.

## Examples

The [_examples](_examples) directory has a runnable program for each shape: a plain directory, embedded templates, several engines in one program, shared function collections, and a static site generator.

## Upgrading

See [CHANGELOG.md](CHANGELOG.md). This release requires Go 1.27, replaces the `Templates` and `Layouts` fields with `Lookup` and the name iterators, and drops go-bindata support in favor of `embed`.

## License

This software is licensed under the [MIT License](LICENSE).
