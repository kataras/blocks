package blocks

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/russross/blackfriday/v2"
	"github.com/valyala/bytebufferpool"
)

type (
	// AssetFunc type declaration for reading content based on a name.
	AssetFunc func(string) ([]byte, error) // key = full pathname, value = a func which should return its contents.
	// AssetNamesFunc type declaration for returning all filenames (no dirs).
	AssetNamesFunc func() []string
	// ExtensionParser type declaration to customize other extension's parsers before passed to the template's one.
	ExtensionParser func([]byte) ([]byte, error)
)

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
	dir               string // ./views
	layoutDir         string // ./views/layouts
	layoutFuncs       template.FuncMap
	defaultLayoutName string // the default layout if it's missing from the `ExecuteTemplate`.
	extension         string // .html
	left, right       string // delims.

	asset      AssetFunc
	assetNames AssetNamesFunc

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
// It loads the templates based on the given "rootDir".
// By default the layout files should be located at "$rootDir/layouts" sub-directory,
// change this behavior can be achieved through `LayoutDir` method before `Load/LoadContext`.
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
// Use the `Assets` method to define custom
// Asset and AssetNames functions (e.g. generated go-bindata contents).
//
// The default extension can be changed through the `Extension` method.
// More extension parsers can be added through the `Extensions` method.
// The left and right delimeters can be customized through its `Delims` method.
// To reload templates on each request (useful for development stage) call its `Reload(true)` method.
func New(rootDir string) *Blocks {
	v := &Blocks{
		dir:       rootDir,
		layoutDir: filepath.Join(rootDir, "layouts"),
		extension: ".html",
		extensionHandler: map[string]ExtensionParser{
			".md": func(b []byte) ([]byte, error) { return blackfriday.Run(b), nil },
		},
		left:  "{{",
		right: "}}",
		// Root "content" for the default one, so templates without layout can still be rendered.
		// Note that, this is parsed, the delims can be customzized later on.
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
// Parse templates on each request.
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

// Option sets options for the templates. Options are described by
// strings, either a simple string or "key=value". There can be at
// most one equals sign in an option string. If the option string
// is unrecognized or otherwise invalid, Option panics.
//
// Known options:
//
// missingkey: Control the behavior during execution if a map is
// indexed with a key that is not present in the map.
//	"missingkey=default" or "missingkey=invalid"
//		The default behavior: Do nothing and continue execution.
//		If printed, the result of the index operation is the string
//		"<no value>".
//	"missingkey=zero"
//		The operation returns the zero value for the map type's element.
//	"missingkey=error"
//		Execution stops immediately with an error.
//
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
	v.Root.Funcs(funcMap)
	return v
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

// LayoutDir sets a custom layouts directory,
// always relative to the root "dir" one.
// Layouts are recognised by their prefix names.
// Defaults to "layouts".
func (v *Blocks) LayoutDir(relToDirLayoutDir string) *Blocks {
	v.layoutDir = filepath.Join(v.dir, relToDirLayoutDir)
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

// Assets sets the function which reads contents based on a filename
// and a function that returns all the filenames.
// These functions are used to parse the corresponding files into templates.
// You do not need to set them when the given "rootDir" was a system directory.
// It's mostly useful when the application contains embedded template files,
// e.g. pass go-bindata's `Asset` and `AssetNames` functions
// to load templates from go-bindata generated content.
func (v *Blocks) Assets(asset AssetFunc, names AssetNamesFunc) *Blocks {
	v.asset = asset
	v.assetNames = names
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

	asset := v.asset
	if asset == nil {
		asset = ioutil.ReadFile
	}

	assetNames := v.assetNames
	if assetNames == nil {
		var names []string
		err := filepath.Walk(v.dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}

			names = append(names, path)
			return nil
		})
		if err != nil {
			return err
		}

		assetNames = func() []string {
			return names
		}
	}

	return v.load(ctx, asset, assetNames)
}

func (v *Blocks) load(ctx context.Context, asset AssetFunc, assetNames AssetNamesFunc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		layouts []string
		mu      sync.RWMutex
	)

	// +---------------------+
	// |   Template Assets   |
	// +---------------------+
	loadAsset := func(assetName string) error {
		if dir := relDir(v.dir); dir != "" && !strings.HasPrefix(filepath.ToSlash(assetName), dir) {
			// If contains a not empty directory and the asset name does not belong there
			// then skip it, useful on bindata assets when they
			// may contain other files that are not templates.
			return nil
		}

		if layoutDir := relDir(v.layoutDir); layoutDir != "" &&
			strings.HasPrefix(filepath.ToSlash(assetName), layoutDir) {
			// it's a layout template file, add it to layouts and skip,
			// in order to add them to each template file.
			mu.Lock()
			layouts = append(layouts, assetName)
			mu.Unlock()
			return nil
		}

		tmplName := trimDir(assetName, v.dir)

		ext := path.Ext(assetName)
		tmplName = strings.TrimSuffix(tmplName, ext)
		extParser := v.extensionHandler[ext]
		hasHandler := extParser != nil // it may exists but if it's nil then we can't use it.
		if v.extension != "" {
			if ext != v.extension && !hasHandler {
				return nil
			}
		}

		contents, err := asset(assetName)
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			break
		}

		if hasHandler {
			contents, err = extParser(contents)
			if err != nil {
				// custom parsers may return a non-nil error,
				// e.g. less or scss files
				// and, yes, they can be used as templates too,
				// because they are wrapped by a template block if necessary.
				return err
			}
		}

		mu.Lock()
		v.Templates[tmplName], err = v.Root.Clone()
		mu.Unlock()
		if err != nil {
			return err
		}

		str := string(contents)

		// should have any kind of template or the whole as content template,
		// if not we will make it as a single template definition.
		if !strings.Contains(str, defineStart(v.left)) && !strings.Contains(str, defineStartNoSpace(v.left)) {
			str = defineContentStart(v.left, v.right) + str + defineContentEnd(v.left, v.right)
		}

		mu.RLock()
		_, err = v.Templates[tmplName].Parse(str)
		mu.RUnlock()
		return err
	}

	var (
		err     error
		wg      sync.WaitGroup
		errOnce sync.Once
	)

	for _, assetName := range assetNames() {
		wg.Add(1)

		go func(assetName string) {
			defer wg.Done()

			if loadErr := loadAsset(assetName); loadErr != nil {
				errOnce.Do(func() {
					err = loadErr
					cancel()
				})
			}
		}(assetName)
	}

	wg.Wait()
	if err != nil {
		return err
	}

	// +---------------------+
	// |       Layouts       |
	// +---------------------+
	loadLayout := func(layout string) error {
		contents, err := asset(layout)
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			break
		}

		name := trimDir(layout, v.layoutDir) // if we want rel-to-the-dir instead we just replace with v.Dir.
		name = strings.TrimSuffix(name, v.extension)
		str := string(contents)

		for _, tmpl := range v.Templates {
			mu.Lock()
			v.Layouts[name], err = tmpl.New(name).Funcs(v.layoutFuncs).Parse(str)
			mu.Unlock()
			if err != nil {
				return err
			}
		}

		return nil
	}

	for _, layout := range layouts {
		wg.Add(1)
		go func(layout string) {
			defer wg.Done()

			if loadErr := loadLayout(layout); loadErr != nil {
				errOnce.Do(func() {
					err = loadErr
					cancel()
				})
			}
		}(layout)
	}

	wg.Wait()

	return err
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
func (v *Blocks) ExecuteTemplate(w io.Writer, tmplName, layoutName string, data interface{}) error {
	if v.reload {
		if err := v.Load(); err != nil {
			return err
		}
	}

	tmpl, ok := v.Templates[tmplName]
	if !ok {
		return ErrNotExist{tmplName}
	}

	// if httpResponseWriter, ok := w.(http.ResponseWriter); ok {
	// check if content-type exists, and if it's not:
	// 	httpResponseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	// }  ^ No, leave it for the caller.

	if layoutName != "" {
		return tmpl.ExecuteTemplate(w, layoutName, data)
	}
	return tmpl.Execute(w, data)
}

// ParseTemplate parses a template based on its "tmplName" name and returns the result.
func (v *Blocks) ParseTemplate(tmplName, layoutName string, data interface{}) (string, error) {
	b := v.bufferPool.Get()
	err := v.ExecuteTemplate(b, tmplName, layoutName, data)
	contents := b.String()
	v.bufferPool.Put(b)
	return contents, err
}

// PartialFunc returns the parsed result of the "partialName" template's "content" block.
func (v *Blocks) PartialFunc(partialName string, data interface{}) (template.HTML, error) {
	contents, err := v.ParseTemplate(partialName, "content", data)
	if err != nil {
		return "", err
	}
	return template.HTML(contents), nil
}

// ContextKeyType is the type which `Set`
// request's context value is using to store
// the current Blocks engine.
//  See `Set` and `Get`.
type ContextKeyType struct{}

// ContextKey is the request's context value for a blocks engine.
//  See `Set` and `Get`.
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

	dir = filepath.ToSlash(dir)
	if dir == "" || dir == "/" {
		return ""
	}

	if dir[0] == '/' {
		return dir[1:]
	}

	return strings.TrimPrefix(dir, "./")
}

func trimDir(s string, dir string) string {
	dir = withSuffix(relDir(dir), "/")
	s = filepath.ToSlash(s)
	return strings.TrimPrefix(s, dir)
}
