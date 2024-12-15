package blocks_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kataras/blocks"
)

func TestMemoryFileSystem(t *testing.T) {
	// Create a new MemoryFileSystem
	mfs := blocks.NewMemoryFileSystem()

	// Define template contents
	mainTemplateContent := []byte(`
        <!DOCTYPE html>
        <html>
        <head>
            <title>{{ .Title }}</title>
        </head>
        <body>
            {{ template "content" . }}
			{{- partial "custom/user/partial" . }}
        </body>
        </html>
    `)

	contentTemplateContent := []byte(`{{ define "content" }}Hello, {{ .Name }}!{{ end }}`)
	partialTemplateContent := []byte(`<h3>Partial</h3>`)

	// Parse templates into the memory file system
	err := mfs.ParseTemplate("layouts/main.html", mainTemplateContent, nil)
	if err != nil {
		t.Fatalf("Failed to parse main.html: %v", err)
	}

	err = mfs.ParseTemplate("index.html", contentTemplateContent, nil)
	if err != nil {
		t.Fatalf("Failed to parse index.html: %v", err)
	}

	err = mfs.ParseTemplate("custom/user/partial.html", partialTemplateContent, nil)
	if err != nil {
		t.Fatalf("Failed to parse partial.html: %v", err)
	}

	// Create a new Blocks instance using the MemoryFileSystem
	views := blocks.New(mfs)

	// Set the main layout file
	views.DefaultLayout("main")

	// Load the templates
	err = views.Load()
	if err != nil {
		t.Fatalf("Failed to load templates: %v", err)
	}

	// Data for template execution
	data := map[string]any{
		"Title": "Test Page",
		"Name":  "World",
	}

	// Execute the template
	var buf bytes.Buffer
	err = views.ExecuteTemplate(&buf, "index", "", data)
	if err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	// Expected output
	expectedOutput := `
        <!DOCTYPE html>
        <html>
        <head>
            <title>Test Page</title>
        </head>
        <body>
            Hello, World! <h3>Partial</h3>
        </body>
        </html>
    `

	// Trim whitespace for comparison
	expected := trimContents(expectedOutput)
	result := trimContents(buf.String())

	if expected != result {
		t.Errorf("Expected output does not match.\nExpected:\n%s\nGot:\n%s", expected, result)
	}
}

func TestYieldFunc(t *testing.T) {
	// register the views, here we register them as part of the code of the shake of the example
	// but you can use the `block.New`'s first input argument to load them from the disk.
	mfs := blocks.NewMemoryFileSystem()
	// define a book layout.
	err := mfs.ParseTemplate("layouts/book.html", []byte(`
<html>
<head>
<title>Book Layout</title>

</head>
<body>
	<h1>[layout] Body content is below...</h1>
	{{- yield . }}
</body>
</html>
`), nil)
	if err != nil {
		t.Fatal(err)
	}

	// define the book index page.
	err = mfs.ParseTemplate("book/index.html", []byte(`<h1>Hello, {{.Name}}!</h1>`), nil)
	if err != nil {
		t.Fatal(err)
	}

	views := blocks.New(mfs)
	if err := views.Load(); err != nil {
		t.Fatal(err)
	}

	expectedOutput := `
<html>
<head>
<title>Book Layout</title>

</head>
<body>
	<h1>[layout] Body content is below...</h1>
	<h1>Hello, World!</h1>
</body>
</html>
`

	// Trim whitespace for comparison
	expected := trimContents(expectedOutput)
	got, err := views.TemplateString("book/index", "book", map[string]any{"Name": "World"})
	if err != nil {
		t.Fatal(err)
	}
	result := trimContents(got)

	if expected != result {
		t.Errorf("Expected output does not match.\nExpected:\n%s\nGot:\n%s", expected, result)
	}
}

func trimContents(s string) string {
	trimLineFunc := func(r rune) bool {
		return r == '\r' || r == '\n' || r == ' ' || r == '\t' || r == '\v' || r == '\f'
	}

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimFunc(line, trimLineFunc)
	}

	return strings.Join(lines, " ")
}
