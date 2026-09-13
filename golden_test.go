package blocks_test

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kataras/blocks"
)

// update rewrites every .golden file with the output the code produces now.
//
//	go test -run TestGolden -update
//
// A golden edited to match new output proves nothing. Regenerate only after reading what
// changed and saying why in CHANGELOG.md.
var update = flag.Bool("update", false, "rewrite the .golden files from the current output")

// render is one ExecuteTemplate call recorded in a golden file.
type render struct {
	tmpl   string
	layout string
	data   any
}

// goldenCase is a set of template files, an engine configuration and the renders to
// record. Keep every case free of map iteration order: one layout per case unless the
// case is about layouts, and no range over a Go map whose order would reach the output.
type goldenCase struct {
	name    string
	files   map[string]string
	setup   func(*blocks.Blocks) // applied before Load
	renders []render
}

func (c goldenCase) build(t *testing.T) *blocks.Blocks {
	t.Helper()

	mfs := blocks.NewMemoryFileSystem()
	for name, contents := range c.files {
		if err := mfs.ParseTemplate(name, []byte(contents), nil); err != nil {
			t.Fatalf("parse template %s: %v", name, err)
		}
	}

	v := blocks.New(mfs)
	if c.setup != nil {
		c.setup(v)
	}
	if err := v.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return v
}

func (c goldenCase) record(t *testing.T, v *blocks.Blocks) string {
	t.Helper()

	var out strings.Builder
	for _, r := range c.renders {
		fmt.Fprintf(&out, "=== %s | %s\n", r.tmpl, r.layout)

		var buf bytes.Buffer
		if err := v.ExecuteTemplate(&buf, r.tmpl, r.layout, r.data); err != nil {
			fmt.Fprintf(&out, "ERROR: %v\n", err)
			continue
		}
		out.WriteString(buf.String())
		out.WriteString("\n")
	}
	return out.String()
}

func TestGolden(t *testing.T) {
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			got := c.record(t, c.build(t))
			compareGolden(t, c.name, got)
		})
	}
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", "golden", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}

	if string(want) != got {
		t.Errorf("output changed.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

var goldenCases = []goldenCase{
	{
		// A layout block carries a default body. A content template that defines the same
		// name replaces it, and one that does not keeps the default.
		name: "block-default-and-override",
		files: map[string]string{
			"layouts/main.html": `<html><head>{{ block "title" . }}Default Title{{ end }}</head>` +
				`<body>{{ template "content" . }}</body></html>`,
			"overrides.html": `{{ define "title" }}Overridden{{ end }}{{ define "content" }}<p>body</p>{{ end }}`,
			"keeps.html":     `<p>plain body</p>`,
		},
		renders: []render{
			{tmpl: "overrides", layout: "main"},
			{tmpl: "keeps", layout: "main"},
		},
	},
	{
		// Every accepted spelling of the layout's content hole. The bare yield and the
		// trim markers are the two whose behavior the later fixes change.
		name: "yield-forms",
		files: map[string]string{
			"layouts/dot.html":      "A\n{{ yield . }}\nB",
			"layouts/bare.html":     "A\n{{ yield }}\nB",
			"layouts/trim.html":     "A\n{{- yield . -}}\nB",
			"layouts/template.html": `A{{ template "content" . }}B`,
			"index.html":            `<p>{{ .Name }}</p>`,
		},
		renders: []render{
			{tmpl: "index", layout: "dot", data: map[string]any{"Name": "World"}},
			{tmpl: "index", layout: "bare", data: map[string]any{"Name": "World"}},
			{tmpl: "index", layout: "trim", data: map[string]any{"Name": "World"}},
			{tmpl: "index", layout: "template", data: map[string]any{"Name": "World"}},
		},
	},
	{
		// partial renders another template's content block, from a content template and
		// from inside a layout.
		name: "partials",
		files: map[string]string{
			"layouts/main.html":    `<body>{{ template "content" . }}<footer>{{ partial "partials/footer" . }}</footer></body>`,
			"partials/footer.html": `<small>{{ .Year }}</small>`,
			"index.html":           `<p>{{ partial "partials/footer" . }}</p>`,
		},
		renders: []render{
			{tmpl: "index", layout: "main", data: map[string]any{"Year": 2026}},
			{tmpl: "index", layout: "", data: map[string]any{"Year": 2026}},
		},
	},
	{
		// HTML comments are stripped, and the leading and trailing whitespace of each
		// file is trimmed, before anything is parsed.
		name: "comments-and-whitespace",
		files: map[string]string{
			"layouts/main.html": "\n\n  <body><!-- layout comment -->{{ template \"content\" . }}</body>  \n\n",
			"index.html":        "\n  <p>kept</p><!-- multi\nline\ncomment -->\n  ",
		},
		renders: []render{{tmpl: "index", layout: "main"}},
	},
	{
		// A .md file goes through the markdown parser and keeps its extension in the
		// template name.
		name: "markdown",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }}</body>`,
			"notes.md":          "# Heading\n\nText with *emphasis* and a [link](https://go.dev).\n",
		},
		renders: []render{
			{tmpl: "notes.md", layout: "main"},
			{tmpl: "notes.md", layout: ""},
		},
	},
	{
		// What the markdown parser does with the constructs a template author reaches for.
		// Raw HTML passing through is the one that matters most: without the unsafe
		// renderer option goldmark replaces it with a comment, and the comment stripper
		// then deletes it, so the markup would vanish with nothing said.
		name: "markdown-features",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }}</body>`,
			"features.md": `## Heading {#custom-id}

| Column | Value |
| --- | --- |
| a | 1 |

Text with ~~strikethrough~~ and a bare link to https://go.dev here.

- [x] done
- [ ] open

Term
: Definition

<div class="raw">raw HTML block</div>

A line with two trailing spaces  
and "quotes" plus an ellipsis...

    indented code block, where {{ 1 }} is still an action
`,
		},
		renders: []render{{tmpl: "features.md", layout: "main"}},
	},
	{
		name: "extension-trimming",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }}</body>`,
			"index.html":        `<p>index</p>`,
		},
		renders: []render{
			{tmpl: "index", layout: "main"},
			{tmpl: "index.html", layout: "main"},
			{tmpl: "index.html", layout: "main.html"},
		},
	},
	{
		name: "default-layout",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }}</body>`,
			"index.html":        `<p>index</p>`,
		},
		setup:   func(v *blocks.Blocks) { v.DefaultLayout("main") },
		renders: []render{{tmpl: "index", layout: ""}},
	},
	{
		name: "custom-layout-dir",
		files: map[string]string{
			"shared/main.html": `<body>{{ template "content" . }}</body>`,
			"index.html":       `<p>index</p>`,
		},
		setup:   func(v *blocks.Blocks) { v.LayoutDir("shared") },
		renders: []render{{tmpl: "index", layout: "main"}},
	},
	{
		name: "root-dir",
		files: map[string]string{
			"data/views/layouts/main.html": `<body>{{ template "content" . }}</body>`,
			"data/views/index.html":        `<p>index</p>`,
			"data/ignored.html":            `<p>ignored</p>`,
		},
		setup:   func(v *blocks.Blocks) { v.RootDir("data/views") },
		renders: []render{{tmpl: "index", layout: "main"}},
	},
	{
		// A template in a nested directory keeps that directory in its name.
		name: "nested-directories",
		files: map[string]string{
			"layouts/main.html":     `<body>{{ template "content" . }}</body>`,
			"admin/users/list.html": `<p>users</p>`,
			"admin/users/edit.html": `<p>edit {{ .ID }}</p>`,
		},
		renders: []render{
			{tmpl: "admin/users/list", layout: "main"},
			{tmpl: "admin/users/edit", layout: "main", data: map[string]any{"ID": 7}},
		},
	},
	{
		// Funcs reaches content templates. A layout that calls the same function fails
		// to load today, which TestLayoutCanCallFuncs covers; LayoutFuncs is how a
		// layout gets one.
		name: "funcs",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }} {{ banner }}</body>`,
			"index.html":        `<p>{{ add 2 3 }} {{ shout .Name }}</p>`,
		},
		setup: func(v *blocks.Blocks) {
			v.Funcs(template.FuncMap{
				"add":   func(i, j int) int { return i + j },
				"shout": strings.ToUpper,
			})
			v.LayoutFuncs(template.FuncMap{"banner": func() string { return "[banner]" }})
		},
		renders: []render{{tmpl: "index", layout: "main", data: map[string]any{"Name": "go"}}},
	},
	{
		// Contextual escaping belongs to html/template, not to this package, but a change
		// to how templates are assembled could lose it. This case fails loudly if it does.
		name: "autoescaping",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }}</body>`,
			"index.html":        `<a href="{{ .URL }}" title="{{ .Title }}">{{ .Body }}</a><script>var x = {{ .Body }};</script>`,
		},
		renders: []render{{tmpl: "index", layout: "main", data: map[string]any{
			"URL":   "javascript:alert(1)",
			"Title": `" onmouseover="alert(1)`,
			"Body":  `<script>alert('pwned')</script>`,
		}}},
	},
	{
		// A missing map key prints its placeholder by default. Under a layout it still
		// does today, because Option never reaches layouts. Group 8 changes that half.
		name: "missingkey-error",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }} layout:{{ .Missing }}</body>`,
			"index.html":        `<p>content:{{ .Missing }}</p>`,
		},
		setup: func(v *blocks.Blocks) { v.Option("missingkey=error") },
		renders: []render{
			{tmpl: "index", layout: "", data: map[string]any{}},
			{tmpl: "index", layout: "main", data: map[string]any{}},
		},
	},
	{
		// A content file that mixes top-level text with a define. html/template lets the
		// last parse of a body win, so the top-level text replaces the layout's body.
		// A sharp edge, recorded so a refactor cannot change it quietly.
		name: "defines-plus-toplevel-text",
		files: map[string]string{
			"layouts/main.html": `<body>{{ template "content" . }}</body>`,
			"index.html":        `{{ define "content" }}<p>defined</p>{{ end }}top level text`,
		},
		renders: []render{{tmpl: "index", layout: "main"}},
	},
}
