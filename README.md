# Blocks

[![build status](https://img.shields.io/travis/com/kataras/blocks/master.svg?style=for-the-badge&logo=travis)](https://travis-ci.com/github/kataras/blocks) [![report card](https://img.shields.io/badge/report%20card-a%2B-ff3333.svg?style=for-the-badge)](https://goreportcard.com/report/github.com/kataras/blocks) [![godocs](https://img.shields.io/badge/go-%20docs-488AC7.svg?style=for-the-badge)](https://pkg.go.dev/github.com/kataras/blocks)

Blocks is a, simple, Go-idiomatic view engine based on [html/template](https://pkg.go.dev/html/template?tab=doc#Template), plus the following features:

- Embedded templates through [go-bindata](https://github.com/go-bindata/go-bindata)
- Load with optional context for cancelation
- Reload templates on development stage
- Full Layouts and Blocks support
- Markdown Content
- Global [FuncMap](https://pkg.go.dev/html/template?tab=doc#FuncMap)

## Installation

The only requirement is the [Go Programming Language](https://golang.org/dl).

```sh
$ go get github.com/kataras/blocks
```

## Getting Started

Import the package:

```go
import "github.com/kataras/blocks"
```

The `blocks` package is fully compatible with the standard library. Use the [New(directory string)](https://pkg.go.dev/github.com/kataras/blocks?tab=doc#New) function to return a fresh Blcoks view engine that renders templates. 

This directory can be used to locate system template files or to select the wanted template files across a range of embedded data (or empty if templates are not prefixed with a root directory).

```go
views := blocks.New("./views")
```

After the initialization and engine's customizations the user SHOULD call its [Load() error](https://pkg.go.dev/github.com/kataras/blocks?tab=doc#Blocks.Load) or [LoadWithContext(context.Context) error](https://pkg.go.dev/github.com/kataras/blocks?tab=doc#Blocks.LoadWithContext) method once in order to parse the files into templates.

```go
err := views.Load()
```

There are several methods to customize the engine, **before `Load`**, including `Delims`, `Option`, `Funcs`, `Extension`, `Assets`, `LayoutDir`, `DefaultLayout` and `Extensions`. You can learn more about those in our [godocs](https://pkg.go.dev/github.com/kataras/blocks?tab=Blocks).

To render a template through a compatible [io.Writer](https://golang.org/pkg/io/#Writer) use the [ExecuteTemplate(w io.Writer, tmplName, layoutName string, data interface{})](https://pkg.go.dev/github.com/kataras/blocks?tab=doc#Blocks.ExecuteTemplate) method.

```go
func handler(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title": "Index Title",
	}

	err := views.ExecuteTemplate(w, "index", "main", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
```

To parse files that are translated as Go code, inside the executable program itself, pass the [go-bindata's generated](https://github.com/go-bindata/go-bindata) `Asset` and `AssetNames` functions to the `Assets` method:

```go
views := blocks.New("./views").Assets(Asset, AssetNames)
```

Please navigate through [_examples](_examples) directory for more.

## License

This software is licensed under the [MIT License](LICENSE).
