# Documentation

## Overview

The `blocks` package is a [Go](https://go.dev) library designed to facilitate the creation and management of HTML templates. It provides a flexible and powerful way to define, load, and render templates, making it easier to build dynamic web applications.

## Features

- **Template Loading**: Load templates from various sources, including file systems and embedded file systems.
- **Template Functions**: Register custom functions to be used within templates.
- **Partial Templates**: Support for partial templates to reuse common template fragments.
- **Layout Management**: Define and use layouts to structure your HTML pages.
- **Automatic Escaping**: Automatically escape HTML output to prevent cross-site scripting (XSS) attacks.
- **Automatic Comment Stripping**: Automatically strip comments.
- **Reloading**: Automatically reload templates during development for faster iteration.

## Installation

To install the `blocks` package, use the following command:

```sh
go get github.com/kataras/blocks@latest
```

## Usage

### Basic Example

Here's a basic example of how to use the `blocks` package to load and render templates:

```go
package main

import (
	"net/http"
	"time"

	"github.com/kataras/blocks"
)

var views = blocks.New("./views").
	Reload(true).
	Funcs(map[string]interface{}{
		"year": func() int {
			return time.Now().Year()
		},
	})

func main() {
	err := views.Load()
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/", index)
	http.HandleFunc("/500", internalServerError)

	println("Now listening on: http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]interface{}{
		"Title": "Page Title",
	}

	err := views.ExecuteTemplate(w, "index", "main", data)
	if err != nil {
		println(err.Error())
	}
}

func internalServerError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)

	data := map[string]interface{}{
		"Code":    http.StatusInternalServerError,
		"Message": "Internal Server Error",
	}
	views.ExecuteTemplate(w, "500", "error", data)
}
```

### Template Functions

You can register custom functions to be used within your templates. Here's an example of how to register and use a custom function:

```go
package main

import (
	"html/template"
	"net/http"

	"github.com/kataras/blocks"
)

var funcs = template.FuncMap{
	"add": func(i, j int) int {
		return i + j
	},
}

func main() {
	views := blocks.New("./views")
	views.Funcs(funcs)

	if err := views.Load(); err != nil {
		panic(err)
	}

	http.HandleFunc("/", calc(views))

	println("Now listening on: http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

func calc(views *blocks.Blocks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		err := views.ExecuteTemplate(w, "calc", "", nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}
```

### Partial Templates

Partial templates allow you to reuse common template fragments. Here's an example of how to define and use partial templates:

```html
<!-- footer.html -->
<h3>Footer Partial {{ if year -}} &copy;{{- year -}} {{end }} </h3>
```

```html
<!-- main.html -->
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{ if .Title }}{{ .Title }}{{ else }}Default Main Title{{ end }}</title>
</head>
<body>
    {{ template "content" . }}

    <footer>{{ partial "partials/footer" .}}</footer>
</body>
</html>
```

## Advanced Usage

### Embedding Templates

You can embed templates using the `embed` package. Here's an example:

```go
package main

import (
	"embed"
	"net/http"
	"time"

	"github.com/kataras/blocks"
)

//go:embed data/*
var embeddedFS embed.FS

var views = blocks.New(embeddedFS).
	RootDir("data/views").
	Reload(true).
	LayoutDir("layouts").
	Funcs(map[string]interface{}{
		"year": func() int {
			return time.Now().Year()
		},
	})

func main() {
	err := views.Load()
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/", index)
	http.HandleFunc("/500", internalServerError)

	println("Now listening on: http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]interface{}{
		"Title": "Page Title",
	}

	err := views.ExecuteTemplate(w, "index", "main", data)
	if err != nil {
		println(err.Error())
	}
}

func internalServerError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)

	data := map[string]interface{}{
		"Code":    http.StatusInternalServerError,
		"Message": "Internal Server Error",
	}
	views.ExecuteTemplate(w, "500", "error", data)
}
```

## Conclusion

The `blocks` package provides a robust and flexible way to manage HTML templates in Go. By leveraging its features, you can build dynamic and maintainable web applications with ease. For more information, refer to the examples provided and the official documentation of the [html/template](https://pkg.go.dev/html/template) package.
