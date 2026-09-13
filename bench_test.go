package blocks_test

import (
	"fmt"
	"io"
	"testing"

	"github.com/kataras/blocks"
)

// benchFiles builds a set the size of a small site: one layout, one partial and n
// content templates. Load cost grows with layouts times content templates, so n is what
// the Load benchmark varies.
func benchFiles(n int) map[string]string {
	files := map[string]string{
		"layouts/main.html": `<html><head><title>{{ block "title" . }}Site{{ end }}</title></head>` +
			`<body>{{ template "content" . }}<footer>{{ partial "partials/footer" . }}</footer></body></html>`,
		"partials/footer.html": `<small>{{ .Year }} {{ .Name }}</small>`,
	}
	for i := range n {
		files[fmt.Sprintf("pages/page%d.html", i)] = fmt.Sprintf(
			`<h1>Page %d</h1><p>{{ .Name }}</p><ul>{{ range .Items }}<li>{{ . }}</li>{{ end }}</ul>`, i)
	}
	return files
}

var benchData = map[string]any{
	"Name":  "World",
	"Year":  2026,
	"Items": []string{"one", "two", "three"},
}

func benchEngine(b *testing.B, n int) *blocks.Blocks {
	b.Helper()

	mfs := blocks.NewMemoryFileSystem()
	for name, contents := range benchFiles(n) {
		if err := mfs.ParseTemplate(name, []byte(contents), nil); err != nil {
			b.Fatal(err)
		}
	}

	v := blocks.New(mfs)
	if err := v.Load(); err != nil {
		b.Fatal(err)
	}
	return v
}

func BenchmarkLoad(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("pages=%d", n), func(b *testing.B) {
			mfs := blocks.NewMemoryFileSystem()
			for name, contents := range benchFiles(n) {
				if err := mfs.ParseTemplate(name, []byte(contents), nil); err != nil {
					b.Fatal(err)
				}
			}
			v := blocks.New(mfs)

			b.ReportAllocs()
			for b.Loop() {
				if err := v.Load(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkExecuteTemplate(b *testing.B) {
	v := benchEngine(b, 10)

	b.ReportAllocs()
	for b.Loop() {
		if err := v.ExecuteTemplate(io.Discard, "pages/page0", "", benchData); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExecuteTemplateWithLayout(b *testing.B) {
	v := benchEngine(b, 10)

	b.ReportAllocs()
	for b.Loop() {
		if err := v.ExecuteTemplate(io.Discard, "pages/page0", "main", benchData); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTemplateString(b *testing.B) {
	v := benchEngine(b, 10)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := v.TemplateString("pages/page0", "main", benchData); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPartialFunc(b *testing.B) {
	v := benchEngine(b, 10)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := v.PartialFunc("partials/footer", benchData); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExecuteTemplateParallel is the shape a web server actually runs: many
// goroutines rendering from one loaded engine.
func BenchmarkExecuteTemplateParallel(b *testing.B) {
	v := benchEngine(b, 10)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := v.ExecuteTemplate(io.Discard, "pages/page0", "main", benchData); err != nil {
				b.Fatal(err)
			}
		}
	})
}
