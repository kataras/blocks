// Package blocks renders HTML pages built from a layout and a content template, on top of
// the standard html/template.
//
// A page is one file. A layout is a file that says where the page goes, with the yield
// action or with the content template action:
//
//	<!-- views/layouts/main.html -->
//	<html>
//	<head><title>{{ .Title }}</title></head>
//	<body>{{ yield . }}</body>
//	</html>
//
//	<!-- views/index.html -->
//	<h1>{{ .Title }}</h1>
//
// Every page is parsed into every layout, so any page can render under any layout:
//
//	views := blocks.New("./views")
//	if err := views.Load(); err != nil {
//		return err
//	}
//
//	err := views.ExecuteTemplate(w, "index", "main", map[string]any{"Title": "Home"})
//
// # Where templates come from
//
// New accepts a directory name, any fs.FS (an embed.FS included), or a MemoryFileSystem
// for templates built at runtime. Embedding is what most programs want, because the
// binary then carries its own templates:
//
//	//go:embed views
//	var embedded embed.FS
//
//	views := blocks.New(embedded).RootDir("views")
//
// # Names
//
// A template is named by its path under the root directory, without the extension, so
// "views/admin/users.html" is "admin/users". Layouts live in "layouts" by default, and
// that directory is trimmed from the name: "views/layouts/main.html" is "main". Both
// names may be given with the extension, and a layout with its directory, so
// "layouts/main.html" works too.
//
// # Blocks, partials and functions
//
// A layout can leave a hole with the block action, and a page can fill it:
//
//	<title>{{ block "title" . }}Default{{ end }}</title>   in the layout
//	{{ define "title" }}Specific{{ end }}                  in the page
//
// The partial function renders another template in place, which is how a footer or a card
// is reused:
//
//	<footer>{{ partial "partials/footer" . }}</footer>
//
// Funcs adds functions for one engine, LayoutFuncs adds functions only layouts can call,
// and Register adds functions every engine gets. A function map is read when Load runs,
// so add the functions first.
//
// # What happens to a file before it is parsed
//
// Leading and trailing whitespace is trimmed. HTML comments are removed, which has to
// happen because the template parser reads a define inside a comment as a real one. A
// file with the .md extension is rendered from markdown to HTML first, and other
// extensions can be handled by registering a parser with Extensions.
//
// # Loading and reloading
//
// Load parses everything once. Reload(true) reparses on every render, for development. A
// reload builds a new set of templates and swaps it in, so a render already in flight
// keeps the set it started with, and a reparse that fails leaves the working templates in
// place and returns the error.
//
// An engine is safe for concurrent use once loaded. Configure it, load it, then serve.
package blocks
