package blocks_test

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/kataras/blocks"
)

// The templates under testdata/views are compiled into the test binary, which is how a
// program that ships as one file carries its templates.
//
//go:embed testdata/views
var embedded embed.FS

// Loading templates from an embed.FS. RootDir selects the directory inside it, so the
// names come out as "index" and "about" rather than "testdata/views/index".
func Example() {
	views := blocks.New(embedded).RootDir("testdata/views")
	if err := views.Load(); err != nil {
		log.Fatal(err)
	}

	data := map[string]any{"Title": "Home", "Name": "Gopher", "Year": 2026}
	if err := views.ExecuteTemplate(os.Stdout, "index", "main", data); err != nil {
		log.Fatal(err)
	}

	// Output:
	// <!DOCTYPE html>
	// <html lang="en">
	// <head><title>Home</title></head>
	// <body>
	// <h1>Home</h1>
	// <p>Welcome, Gopher.</p>
	// <footer><small>2026 Blocks</small></footer>
	// </body>
	// </html>
}

// Templates built in memory, with no files on disk. The function map given to
// ParseTemplate is added to the engine, so every template can call greet.
func ExampleNewMemoryFileSystem() {
	mfs := blocks.NewMemoryFileSystem()

	err := mfs.ParseTemplate("layouts/main.html",
		[]byte(`<main>{{ yield . }}</main>`), nil)
	if err != nil {
		log.Fatal(err)
	}

	err = mfs.ParseTemplate("index.html", []byte(`<p>{{ greet .Name }}</p>`),
		template.FuncMap{"greet": func(name string) string { return "Hello, " + name }})
	if err != nil {
		log.Fatal(err)
	}

	views := blocks.New(mfs)
	if err = views.Load(); err != nil {
		log.Fatal(err)
	}

	out, err := views.TemplateString("index", "main", map[string]any{"Name": "Gopher"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)

	// Output:
	// <main><p>Hello, Gopher</p></main>
}

// A directory on disk, the shape most programs start with. DefaultLayout means
// ExecuteTemplate can be called with an empty layout name.
func ExampleNew_directory() {
	views := blocks.New("./testdata/views").DefaultLayout("main")
	if err := views.Load(); err != nil {
		log.Fatal(err)
	}

	data := map[string]any{"Title": "About", "Year": 2026}
	if err := views.ExecuteTemplate(os.Stdout, "about", "", data); err != nil {
		log.Fatal(err)
	}

	// Output:
	// <!DOCTYPE html>
	// <html lang="en">
	// <head><title>About</title></head>
	// <body>
	// <h1>About</h1>
	// <footer><small>2026 Blocks</small></footer>
	// </body>
	// </html>
}

// Rendering from an HTTP handler. Set the content type yourself: the engine writes the
// body and nothing else.
func ExampleBlocks_ExecuteTemplate() {
	views := blocks.New(embedded).RootDir("testdata/views")
	if err := views.Load(); err != nil {
		log.Fatal(err)
	}

	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		data := map[string]any{"Title": "Home", "Name": "Gopher", "Year": 2026}
		if err := views.ExecuteTemplate(w, "index", "main", data); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	}

	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodGet, "/", nil))
	fmt.Println(w.Code, w.Header().Get("Content-Type"))

	// Output:
	// 200 text/html; charset=utf-8
}

// What was loaded, by name.
func ExampleBlocks_TemplateNames() {
	views := blocks.New(embedded).RootDir("testdata/views")
	if err := views.Load(); err != nil {
		log.Fatal(err)
	}

	for name := range views.TemplateNames() {
		fmt.Println(name)
	}
	for name := range views.LayoutNames() {
		fmt.Println("layout:", name)
	}

	// Output:
	// about
	// index
	// partials/footer
	// layout: main
}

// Lookup reaches the parsed template, for rendering one block on its own. Treat what it
// returns as read-only.
func ExampleBlocks_Lookup() {
	views := blocks.New(embedded).RootDir("testdata/views")
	if err := views.Load(); err != nil {
		log.Fatal(err)
	}

	tmpl := views.Lookup("partials/footer", "")
	if err := tmpl.ExecuteTemplate(os.Stdout, "content", map[string]any{"Year": 2026}); err != nil {
		log.Fatal(err)
	}

	// Output:
	// <small>2026 Blocks</small>
}

// Different engines for different groups of routes, reached through the request context.
func ExampleSet() {
	admin := blocks.New(embedded).RootDir("testdata/views")
	if err := admin.Load(); err != nil {
		log.Fatal(err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		views := blocks.Get(r)

		data := map[string]any{"Title": "Admin", "Year": 2026}
		if err := views.ExecuteTemplate(w, "about", "main", data); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	})

	w := httptest.NewRecorder()
	blocks.Set(admin)(handler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	fmt.Println(w.Code)

	// Output:
	// 200
}
