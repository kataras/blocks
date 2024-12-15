package blocks

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/russross/blackfriday/v2"
	"github.com/valyala/bytebufferpool"
)

// ExtensionParser type declaration to customize other extension's parsers before passed to the template's one.
type ExtensionParser func([]byte) ([]byte, error)

// ErrNotExist reports whether a template was not found in the parsed templates tree.
type ErrNotExist struct {
	Name string
}

// Error implements the `error` interface.
func (e ErrNotExist) Error() string {
	return fmt.Sprintf("template '%s' does not exist", e.Name)
}

// Blocks is the main structure which
// holds the necessary information and options
// to parse and render templates.
// See `New` to initialize a new one.
type Blocks struct {
	// the file system to load templates from.
	// The "rootDir" field can be used to select a specific directory from this file system.
	fs fs.FS

	rootDir           string // it always set to "/" as the RootDir method changes the filesystem to sub one.
	layoutDir         string // the default is "/layouts". Empty, means the end-developer should provide the relative to root path of the layout template.
	layoutFuncs       template.FuncMap
	tmplFuncs         template.FuncMap
	defaultLayoutName string // the default layout if it's missing from the `ExecuteTemplate`.
	extension         string // .html
	left, right       string // delims.

	// extensionHandler can handle other file extensions rathen than the main one,
	// The default contains an entry of ".md" for `blackfriday.Run`.
	extensionHandler map[string]ExtensionParser // key = extension with dot, value = parser.

	// parse the templates on each request.
	reload     bool
	mu         sync.RWMutex
	bufferPool *bytebufferpool.Pool

	// Root, Templates and Layouts can be accessed after `Load`.
	Root               *template.Template
	Templates, Layouts map[string]*template.Template
}

// New returns a fresh Blocks engine instance.
// It loads the templates based on the given fs FileSystem (or string).
// To set a default layout name for an empty layout definition on `ExecuteTemplate/ParseTemplate`
// use the `DefaultLayout` method.
//
// The user can customize various options through the Blocks methods.
// The user of this engine MUST call its `Load/LoadWithContext` method once
// before any call of `ExecuteTemplate` and `ParseTemplate`.
//
// Global functions registered through `Register` package-level function
// will be inherited from this engine. To add a function map to this engine
// use its `Funcs` method.
//
// The default extension can be changed through the `Extension` method.
// More extension parsers can be added through the `Extensions` method.
// The left and right delimeters can be customized through its `Delims` method.
// To reload templates on each request (useful for development stage) call its `Reload(true)` method.
//
// Usage:
// New("./views") or
// New(http.Dir("./views")) or
// New(embeddedFS) or New(AssetFile()) for embedded data.
func New(fs any) *Blocks {
	v := &Blocks{
		fs:        getFS(fs),
		layoutDir: "/layouts",
		extension: ".html",
		extensionHandler: map[string]ExtensionParser{
			".md": func(b []byte) ([]byte, error) { return blackfriday.Run(b), nil },
		},
		left:  "{{",
		right: "}}",
		// Root "content" for the default one, so templates without layout can still be rendered.
		// Note that, this is parsed, the delims can be configured later on.
		Root: template.Must(template.New("root").
			Parse(`{{ define "root" }} {{ template "content" . }} {{ end }}`)),
		Templates:  make(map[string]*template.Template),
		Layouts:    make(map[string]*template.Template),
		reload:     false,
		bufferPool: new(bytebufferpool.Pool),
	}

	v.Root.Funcs(translateFuncs(v, builtins))

	return v
}

// Reload will turn on the `Reload` setting, for development use.
// It forces the `ExecuteTemplate` to re-parse the templates on each incoming request.
func (v *Blocks) Reload(b bool) *Blocks {
	v.reload = b
	return v
}

var (
	defineStart = func(left string) string {
		return fmt.Sprintf("%s define", left)
	}
	defineStartNoSpace = func(left string) string {
		return fmt.Sprintf("%sdefine", left)
	}
	defineContentStart = func(left, right string) string {
		return fmt.Sprintf(`%sdefine "content"%s`, left, right)
	}
	defineContentEnd = func(left, right string) string {
		return fmt.Sprintf("%send%s", left, right)
	}
)

// Delims sets the action delimiters to the specified strings, to be used in
// Load. Nested template
// definitions will inherit the settings. An empty delimiter stands for the
// corresponding default: {{ or }}.
// The return value is the engine, so calls can be chained.
func (v *Blocks) Delims(left, right string) *Blocks {
	v.left = left
	v.right = right
	v.Root.Delims(left, right)
	return v
}

// This should be on and should not change, as the go parser will try to parse
// define blocks inside comments too.
//
// RemoveComments sets the engine to remove all HTML comments from the templates.
// This is useful when you want to serve the templates as HTML without comments.
// func (v *Blocks) RemoveComments(b bool) *Blocks {
// 	v.removeComments = b
// 	return v
// }

// Option sets options for the templates. Options are described by
// strings, either a simple string or "key=value". There can be at
// most one equals sign in an option string. If the option string
// is unrecognized or otherwise invalid, Option panics.
//
// Known options:
//
// missingkey: Control the behavior during execution if a map is
// indexed with a key that is not present in the map.
//
//	"missingkey=default" or "missingkey=invalid"
//		The default behavior: Do nothing and continue execution.
//		If printed, the result of the index operation is the string
//		"<no value>".
//	"missingkey=zero"
//		The operation returns the zero value for the map type's element.
//	"missingkey=error"
//		Execution stops immediately with an error.
func (v *Blocks) Option(opt ...string) *Blocks {
	v.Root.Option(opt...)
	return v
}

// Funcs adds the elements of the argument map to the root template's function map.
// It must be called before the engine is loaded.
// It panics if a value in the map is not a function with appropriate return
// type. However, it is legal to overwrite elements of the map. The return
// value is the engine, so calls can be chained.
//
// The default function map contains a single element of "partial" which
// can be used to render templates directly.
func (v *Blocks) Funcs(funcMap template.FuncMap) *Blocks {
	if v.tmplFuncs == nil {
		v.tmplFuncs = funcMap
		return v
	}

	for name, fn := range funcMap {
		v.tmplFuncs[name] = fn
	}

	v.Root.Funcs(funcMap)
	return v
}

// AddFunc adds a function to the root template's function map.
func (v *Blocks) AddFunc(funcName string, fn any) {
	if v.tmplFuncs == nil {
		v.tmplFuncs = make(template.FuncMap)
	}

	v.tmplFuncs[funcName] = fn
	v.Root.Funcs(v.tmplFuncs)
}

// Ext returns the template file extension (with dot).
func (v *Blocks) Ext() string {
	return v.extension
}

// LayoutFuncs same as `Funcs` but this map's elements will be added
// only to the layout templates. It's legal to override elements of the root `Funcs`.
func (v *Blocks) LayoutFuncs(funcMap template.FuncMap) *Blocks {
	if v.layoutFuncs == nil {
		v.layoutFuncs = funcMap
		return v
	}

	for name, fn := range funcMap {
		v.layoutFuncs[name] = fn
	}

	return v
}

// RootDir sets the directory to use as the root one inside the provided File System.
func (v *Blocks) RootDir(root string) *Blocks {
	if v.fs != nil && root != "" && root != "/" && root != "." {
		sub, err := fs.Sub(v.fs, root)
		if err != nil {
			panic(err)
		}

		v.fs = sub
	}

	// v.rootDir = filepath.ToSlash(root)
	// v.layoutDir = path.Join(root, v.layoutDir)
	return v
}

// LayoutDir sets a custom layouts directory,
// always relative to the "rootDir" one.
// This can be used to trim the layout directory from the template's name,
// for example: layouts/main -> main on ExecuteTemplate's "layoutName" argument.
// Defaults to "layouts".
func (v *Blocks) LayoutDir(relToDirLayoutDir string) *Blocks {
	v.layoutDir = filepath.ToSlash(relToDirLayoutDir)
	return v
}

// DefaultLayout sets the "layoutName" to be used
// when the `ExecuteTemplate`'s one is empty.
func (v *Blocks) DefaultLayout(layoutName string) *Blocks {
	v.defaultLayoutName = layoutName
	return v
}

// Extension sets the template file extension (with dot).
// Defaults to ".html".
func (v *Blocks) Extension(ext string) *Blocks {
	v.extension = ext
	return v
}

// Extensions registers a parser that will be called right before
// a file's contents parsed as a template.
// The "ext" should start with dot (.), e.g. ".md".
// The "parser" is a function which accepts the original file's contents
// and should return the parsed ones, e.g. return markdown.Run(contents), nil.
//
// The default underline map contains a single element of ".md": markdown.Run,
// which is responsible to convert markdown files to html right before its contents
// are given to the template's parser.
//
// To override an extension handler pass a nil "parser".
func (v *Blocks) Extensions(ext string, parser ExtensionParser) *Blocks {
	v.extensionHandler[ext] = parser
	return v
}

// Load parses the templates, including layouts,
// through the html/template standard package into the Blocks engine.
func (v *Blocks) Load() error {
	return v.LoadWithContext(context.Background())
}

// LoadWithContext accepts a context that can be used for load cancelation, deadline/timeout.
// It parses the templates, including layouts,
// through the html/template standard package into the Blocks engine.
func (v *Blocks) LoadWithContext(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	clearMap(v.Templates)
	clearMap(v.Layouts)

	return v.load(ctx)
}

func (v *Blocks) load(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	filesMap, err := readFiles(ctx, v.fs, v.rootDir)
	if err != nil {
		return err
	}

	if len(filesMap) == 0 {
		return fmt.Errorf("no template files found")
	}

	// templatesContents is used to keep the contents of each content template in order
	// to be parsed on each layout, so all content templates have all layouts available,
	// and all layouts can inject all content templates.
	contentTemplates := make(map[string]string)
	// layoutTemplates is used to keep the contents of each layout template.
	layoutTemplates := make(map[string]string)

	// collect all content and layout template contents.
	for filename, data := range filesMap {
		ext := path.Ext(filename)
		if extParser := v.extensionHandler[ext]; extParser != nil {
			data, err = extParser(data) // let the parser modify the contents.
			if err != nil {
				// custom parsers may return a non-nil error,
				// e.g. less or scss files
				// and, yes, they can be used as templates too,
				// because they are wrapped by a template block if necessary.
				return err
			}
		} else if ext != v.extension {
			continue // extension not match with the given template extension and the extension handler is nil.
		}

		contents := string(data)
		// Remove HTML comments.
		contents = removeComments(contents)

		tmplName := trimDir(filename, v.rootDir)
		tmplName = strings.TrimPrefix(tmplName, "/")
		tmplName = strings.TrimSuffix(tmplName, v.extension)

		if isLayoutTemplate(contents) {
			// Replace any {{ yield . }} with {{ template "content" . }}.
			contents = replaceYieldWithTemplateContent(contents)
			// Remove any given layout dir.
			tmplName = trimDir(tmplName, v.layoutDir)
			layoutTemplates[tmplName] = contents
			continue
		}

		// Inject the define content block.
		if !strings.Contains(contents, defineStart(v.left)) && !strings.Contains(contents, defineStartNoSpace(v.left)) {
			contents = defineContentStart(v.left, v.right) + contents + defineContentEnd(v.left, v.right)
		}

		contentTemplates[tmplName] = contents
	}

	// Load the content templates first.
	for tmplName, contents := range contentTemplates {
		tmpl, err := v.Root.Clone()
		if err != nil {
			return err
		}

		_, err = tmpl.Funcs(v.tmplFuncs).Parse(contents)
		if err != nil {
			return fmt.Errorf("%w: %s: %s", err, tmplName, contents)
		}

		v.Templates[tmplName] = tmpl
	}

	// Load the layout templates.
	layoutBuiltinFuncs := translateFuncs(v, builtins)
	for tmplName, contents := range layoutTemplates {
		for contentTmplName, contentTmplContents := range contentTemplates {
			// Make new layout template for each of the content templates,
			// the key of the layout in map will be the layoutName+tmplName.
			// So each template owns all layouts. This fixes the issue with the new {{ block }} and the usual {{ define }} directives.
			layoutTmpl, err := template.New(tmplName).Funcs(layoutBuiltinFuncs).Funcs(v.layoutFuncs).Parse(contents)
			if err != nil {
				return fmt.Errorf("%w: for layout: %s", err, tmplName)
			}

			_, err = layoutTmpl.Funcs(v.tmplFuncs).Parse(contentTmplContents)
			if err != nil {
				return fmt.Errorf("%w: layout: %s: for template: %s", err, tmplName, contentTmplName)
			}

			key := makeLayoutTemplateName(contentTmplName, tmplName)
			v.Layouts[key] = layoutTmpl
		}
	}

	return nil
}

// ExecuteTemplate applies the template associated with "tmplName"
// to the specified "data" object and writes the output to "w".
// If an error occurs executing the template or writing its output,
// execution stops, but partial results may already have been written to
// the output writer.
//
// If "layoutName" and "v.defaultLayoutName" are both empty then
// the template is executed without a layout.
//
// A template may be executed safely in parallel, although if parallel
// executions share a Writer the output may be interleaved.
func (v *Blocks) ExecuteTemplate(w io.Writer, tmplName, layoutName string, data any) error {
	if v.reload {
		if err := v.Load(); err != nil {
			return err
		}
	}

	if layoutName == "" {
		layoutName = v.defaultLayoutName
	}

	return v.executeTemplate(w, tmplName, layoutName, data)
}

func (v *Blocks) executeTemplate(w io.Writer, tmplName, layoutName string, data any) error {
	tmplName = strings.TrimSuffix(tmplName, v.extension) // trim any extension provided by mistake or by migrating from other engines.

	if layoutName != "" {
		layoutName = strings.TrimSuffix(layoutName, v.extension)
		layoutName = strings.TrimPrefix(layoutName, v.layoutDir)
		layoutName = strings.TrimPrefix(layoutName, "/")
		tmpl := v.getTemplateWithLayout(tmplName, layoutName)
		if tmpl == nil {
			return ErrNotExist{layoutName}
		}

		return tmpl.Execute(w, data)
	}

	tmpl, ok := v.Templates[tmplName]
	if !ok {
		return ErrNotExist{tmplName}
	}

	// if httpResponseWriter, ok := w.(http.ResponseWriter); ok {
	// check if content-type exists, and if it's not:
	// 	httpResponseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	// }  ^ No, leave it for the caller.

	// if layoutName != "" {
	// 	return tmpl.ExecuteTemplate(w, layoutName, data)
	// }
	return tmpl.Execute(w, data)
}

// TemplateString executes a template based on its "tmplName" name and returns its contents result.
// Note that, this does not reload the templates on each call if Reload was set to true.
// To refresh the templates you have to manually call the `Load` upfront.
func (v *Blocks) TemplateString(tmplName, layoutName string, data any) (string, error) {
	b := v.bufferPool.Get()
	// use the unexported method so it does not re-reload the templates on each partial one
	// when Reload was set to true.
	err := v.executeTemplate(b, tmplName, layoutName, data)
	contents := b.String()
	v.bufferPool.Put(b)
	return contents, err
}

// PartialFunc returns the parsed result of the "partialName" template's "content" block.
func (v *Blocks) PartialFunc(partialName string, data any) (template.HTML, error) {
	// contents, err := v.ParseTemplate(partialName, "content", data)
	// if err != nil {
	// 	return "", err
	// }
	contents, err := v.TemplateString(partialName, "", data)
	if err != nil {
		return "", err
	}
	return template.HTML(contents), nil
}

// ContextKeyType is the type which `Set`
// request's context value is using to store
// the current Blocks engine.
//
//	See `Set` and `Get`.
type ContextKeyType struct{}

// ContextKey is the request's context value for a blocks engine.
//
//	See `Set` and `Get`.
var ContextKey ContextKeyType

// Set returns a handler wrapper which sets the current
// view engine to this "v" Blocks.
// Useful when the caller needs multiple Blocks engine instances per group of routes.
// Note that this is entirely optional, the caller could just wrap a function of func(v *Blocks)
// and return a handler which will directly use it.
// See `Get` too.
func Set(v *Blocks) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(context.WithValue(r.Context(), ContextKey, v))
			next.ServeHTTP(w, r)
		})
	}
}

// Get retrieves the associated Blocks view engine retrieved from the request's context.
// See `Set` too.
func Get(r *http.Request) *Blocks {
	value := r.Context().Value(ContextKey)
	if value == nil {
		return nil
	}

	v, ok := value.(*Blocks)
	if !ok {
		return nil
	}

	return v
}

func withSuffix(s string, suf string) string {
	if len(s) == 0 {
		return ""
	}

	if !strings.HasSuffix(s, suf) {
		s += suf
	}

	return s
}

func relDir(dir string) string {
	if dir == "." {
		return ""
	}

	if dir == "" || dir == "/" {
		return ""
	}

	return strings.TrimPrefix(strings.TrimPrefix(dir, "."), "/")
}

func trimDir(s string, dir string) string {
	if dir == "" {
		return s
	}
	dir = withSuffix(relDir(dir), "/")
	return strings.TrimPrefix(s, dir)
}

func (v *Blocks) getTemplateWithLayout(tmplName, layoutName string) *template.Template {
	key := makeLayoutTemplateName(tmplName, layoutName)
	return v.Layouts[key]
}

func makeLayoutTemplateName(tmplName, layoutName string) string {
	return layoutName + tmplName
}

// Regular expression to match HTML comments.
var matchHTMLCommentsRegex = regexp.MustCompile(`<!--[\s\S]*?-->`)

func removeComments(str string) string {
	return matchHTMLCommentsRegex.ReplaceAllString(str, "")
}

var yieldMatchRegex = regexp.MustCompile(`{{-?\s*yield\s*(.*?)\s*-?}}`)

// replaceYieldWithTemplateContent replaces any {{ yield . }} or similar patterns with {{ template "content" . }}.
func replaceYieldWithTemplateContent(input string) string {
	return yieldMatchRegex.ReplaceAllString(input, `{{ template "content" $1 }}`)
}

// Regex pattern to match various forms of {{ template "content" ... }} and {{ yield ... }}
var layoutPatternRegex = regexp.MustCompile(`{{-?\s*(template\s*"content"\s*[^}]*|yield\s*[^}]*)\s*-?}}`)

// isLayoutTemplate checks if the template contents indicate it is a layout template.
func isLayoutTemplate(contents string) bool {
	return layoutPatternRegex.MatchString(contents)
}

func clearMap[M ~map[K]V, K comparable, V any](m M) {
	for k := range m {
		delete(m, k)
	}
}
