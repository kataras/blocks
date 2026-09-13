package blocks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"iter"
	"maps"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
)

// ExtensionParser converts a file's contents before they are parsed as a template. It is
// how a file that is not HTML, such as markdown, becomes one.
type ExtensionParser func([]byte) ([]byte, error)

// ErrNoTemplates is returned by Load when the file system holds no file the engine can
// use, which usually means the directory or the extension is wrong.
var ErrNoTemplates = errors.New("blocks: no template files found")

// ErrNotExist reports that a template or a layout is not among the parsed ones. It
// matches fs.ErrNotExist, so errors.Is(err, fs.ErrNotExist) is true.
type ErrNotExist struct {
	// Name is the template that was asked for.
	Name string
	// Layout is the layout that was asked for, empty when there was none.
	Layout string
	// IsLayout reports whether the layout is the part that is missing.
	IsLayout bool
}

// Error implements the error interface.
func (e ErrNotExist) Error() string {
	if e.IsLayout {
		return fmt.Sprintf("blocks: layout '%s' does not exist", e.Layout)
	}

	if e.Layout != "" {
		return fmt.Sprintf("blocks: template '%s' does not exist for layout '%s'", e.Name, e.Layout)
	}

	return fmt.Sprintf("blocks: template '%s' does not exist", e.Name)
}

// Is reports that this error matches fs.ErrNotExist.
func (e ErrNotExist) Is(target error) bool { return target == fs.ErrNotExist }

// LoadError reports that one file could not be parsed. It names the file and wraps the
// error from html/template.
//
// It never carries the file's contents. A caller that hands err.Error() to an HTTP
// response would otherwise publish the template source.
type LoadError struct {
	// File is the name of the file that failed, as it appears in the file system.
	File string
	// Err is the underlying parse error.
	Err error
}

// Error implements the error interface.
func (e *LoadError) Error() string { return fmt.Sprintf("blocks: %s: %s", e.File, e.Err) }

// Unwrap returns the underlying parse error.
func (e *LoadError) Unwrap() error { return e.Err }

// FuncMapper is implemented by a file system that carries template functions of its own.
// An engine asks for them at every Load and adds them to the function map, under anything
// given to Funcs. MemoryFileSystem implements it, which is how the function map passed to
// ParseTemplate reaches the templates.
type FuncMapper interface {
	TemplateFuncs() template.FuncMap
}

// config is everything a Load reads. It is copied under cfgMu at the start of a build, so
// a setter called from another goroutine cannot change a build halfway through.
type config struct {
	fsys fs.FS

	layoutDir         string // "layouts" by default, trimmed from layout names.
	defaultLayoutName string // used when ExecuteTemplate is given an empty layout.
	extension         string // ".html" by default.
	left, right       string // the action delimiters.

	options     []string
	tmplFuncs   template.FuncMap
	layoutFuncs template.FuncMap

	// extensionHandler maps a file extension, with its dot, to the parser that converts
	// such a file before it is parsed as a template.
	extensionHandler map[string]ExtensionParser

	// funcMapper is the file system given to New, kept when it carries functions. It is
	// captured there because RootDir wraps the file system in fs.Sub, which hides the
	// concrete type this interface lives on.
	funcMapper FuncMapper
}

// fsFuncs returns the functions the file system carries, if any.
func (c config) fsFuncs() template.FuncMap {
	if c.funcMapper == nil {
		return nil
	}
	return c.funcMapper.TemplateFuncs()
}

// clone copies the configuration deeply enough that a build is unaffected by a later
// setter call.
func (c config) clone() config {
	c.options = slices.Clone(c.options)
	c.tmplFuncs = maps.Clone(c.tmplFuncs)
	c.layoutFuncs = maps.Clone(c.layoutFuncs)
	c.extensionHandler = maps.Clone(c.extensionHandler)
	return c
}

// patterns are the regular expressions that read a file, built from one engine
// delimiters so that Delims changes what they match.
type patterns struct {
	define *regexp.Regexp
	layout *regexp.Regexp
	yield  *regexp.Regexp

	left, right string
}

// patternCache keeps one patterns value per pair of delimiters. Compiling three
// expressions on every Load is wasted work in reload mode, where Load runs per request,
// and almost every program uses one pair for the life of the process.
var patternCache sync.Map // map[string]patterns, keyed by left + 0x00 + right.

// patternsFor returns the cached patterns for a pair of delimiters, compiling them the
// first time they are seen.
func patternsFor(left, right string) patterns {
	key := left + string(rune(0)) + right
	if cached, ok := patternCache.Load(key); ok {
		return cached.(patterns)
	}

	pat := newPatterns(left, right)
	patternCache.Store(key, pat)
	return pat
}

// newPatterns compiles the three expressions for a pair of delimiters. The delimiters are
// quoted, so a delimiter made of regular expression metacharacters, such as "[[", is
// matched literally.
func newPatterns(left, right string) patterns {
	l, r := regexp.QuoteMeta(left), regexp.QuoteMeta(right)

	return patterns{
		// A define action, with or without a trim marker. A file that has one already
		// says where its content block is, so nothing is wrapped around it.
		define: regexp.MustCompile(l + `-?\s*define\b`),
		// What makes a file a layout: somewhere to put a content template. The word
		// boundary keeps a function named yielding from turning a page into a layout.
		layout: regexp.MustCompile(l + `-?\s*(?:template\s*"content"|yield\b)`),
		// A yield action, capturing its trim markers and its argument.
		yield: regexp.MustCompile(l + `(-?)\s*yield\b\s*(.*?)\s*(-?)` + r),
		left:  left,
		right: right,
	}
}

// replaceYield rewrites each yield into the template action it is shorthand for, keeping
// the trim markers that were written around it.
//
// A yield with no argument passes the dot, so the content template sees the same data as
// the layout around it.
func (p patterns) replaceYield(input string) string {
	return p.yield.ReplaceAllStringFunc(input, func(match string) string {
		groups := p.yield.FindStringSubmatch(match)
		leftTrim, argument, rightTrim := groups[1], groups[2], groups[3]

		if argument == "" {
			argument = "."
		}

		return p.left + leftTrim + ` template "content" ` + argument + ` ` + rightTrim + p.right
	})
}

// wrapContent puts a content define around a file that does not have one.
func (p patterns) wrapContent(text string) string {
	if p.define.MatchString(text) {
		return text
	}

	return p.left + `define "content"` + p.right + text + p.left + "end" + p.right
}

// layoutKey identifies one content template rendered inside one layout. It is a struct
// rather than the two names concatenated, because concatenating them made layout "ab"
// with template "c" collide with layout "a" with template "bc".
type layoutKey struct {
	layout string
	name   string
}

// snapshot is one finished parse. It is never modified after it is published, so readers
// take no lock: they load the pointer once and use that snapshot for the whole render,
// even while a reload is replacing it.
type snapshot struct {
	templates map[string]*template.Template
	layouts   map[layoutKey]*template.Template

	names       []string // sorted template names.
	layoutNames []string // sorted layout names.

	extension string // the name normalization this snapshot was built with.
	layoutDir string
}

// Blocks parses a set of template files and renders them, on their own or inside a
// layout. Create one with New, configure it, then call Load once before rendering.
//
// A Blocks is safe for concurrent use once loaded, and Reload(true) stays safe: a reload
// builds a new set of templates and swaps it in, so a render already in flight keeps the
// set it started with.
type Blocks struct {
	// cfgMu guards cfg. It is held briefly, by the setters and by the start of a build.
	cfgMu sync.Mutex
	cfg   config

	// loadMu serializes builds. loadCount and lastErr are guarded by it.
	loadMu    sync.Mutex
	loadCount uint64
	lastErr   error

	// loadCountAtomic mirrors loadCount for a reader that has not taken loadMu yet, which
	// is how a burst of reload-mode requests collapses into one build.
	loadCountAtomic atomic.Uint64

	reload atomic.Bool
	snap   atomic.Pointer[snapshot]

	// bufferPool holds *bytes.Buffer for TemplateString, and so for every partial.
	bufferPool sync.Pool
}

// New returns an engine that loads templates from fsOrDir, which may be:
//
//   - a directory name, such as "./views"
//   - an fs.FS, including an embed.FS and anything fs.Sub returns
//   - a *MemoryFileSystem, for templates built at runtime
//
// Embedding is the usual choice for a program that ships as a single binary:
//
//	//go:embed views
//	var embedded embed.FS
//
//	views := blocks.New(embedded).RootDir("views")
//
// The engine is configured through its methods, which return the engine so calls can be
// chained, and which must be called before Load. Load parses the files, and nothing
// renders until it has run.
//
// Functions registered with the package level Register are inherited by every engine. Use
// Funcs for functions this engine alone should have.
//
// It panics if fsOrDir is none of the types above.
func New(fsOrDir any) *Blocks {
	funcMapper, _ := fsOrDir.(FuncMapper)

	return &Blocks{
		cfg: config{
			fsys:       getFS(fsOrDir),
			funcMapper: funcMapper,
			layoutDir:  "layouts",
			extension:  ".html",
			left:       "{{",
			right:      "}}",
			extensionHandler: map[string]ExtensionParser{
				".md": newMarkdownParser(),
			},
		},
	}
}

// newMarkdownParser returns the parser registered for ".md" files.
//
// The options line up with what a template author expects of a markdown file:
//
//   - GFM adds tables, strikethrough, task lists and bare link detection.
//   - Definition lists and the typographer (curly quotes, ellipses) match what this
//     package rendered before goldmark.
//   - WithAttribute allows a heading identifier, written as {#id}.
//   - WithUnsafe keeps raw HTML in the output. Without it goldmark replaces a block of
//     HTML with a comment, and the comment stripper then deletes it, so the markup
//     disappears with nothing said.
//   - WithXHTML closes void elements, so a line break is <br /> as it was before.
//
// Template actions inside a markdown file are rendered after the markdown is converted.
// A code block holding {{ and }} is still read as an action, which is worth knowing
// before documenting this package in its own templates.
func newMarkdownParser() ExtensionParser {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.DefinitionList, extension.Typographer),
		goldmark.WithParserOptions(parser.WithAttribute()),
		goldmark.WithRendererOptions(goldmarkhtml.WithUnsafe(), goldmarkhtml.WithXHTML()),
	)

	return func(contents []byte) ([]byte, error) {
		var buf bytes.Buffer
		if err := md.Convert(contents, &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
}

// rootDefinition is the template every content template is parsed into. It gives a
// template without a layout something to execute: the "content" block on its own.
const rootDefinition = `{{ define "root" }} {{- template "content" . -}} {{ end }}`

// Reload makes ExecuteTemplate reparse the templates on every call, which is what you
// want while editing them and not in production. A failed reparse keeps the templates
// that already work and returns the error.
func (v *Blocks) Reload(b bool) *Blocks {
	v.reload.Store(b)
	return v
}

// Delims sets the action delimiters, which default to "{{" and "}}". An empty delimiter
// keeps its default. Layout detection follows the delimiters, so a layout written with
// custom ones is still recognized.
//
// It panics if a delimiter is not valid UTF-8. The delimiters become part of the
// expressions that read each file, and an expression cannot hold an invalid rune.
func (v *Blocks) Delims(left, right string) *Blocks {
	if !utf8.ValidString(left) || !utf8.ValidString(right) {
		panic(fmt.Errorf("blocks: Delims: %q and %q: a delimiter must be valid UTF-8", left, right))
	}

	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	if left != "" {
		v.cfg.left = left
	}
	if right != "" {
		v.cfg.right = right
	}
	return v
}

// Option sets options for the templates, each a plain string or "key=value". The options
// reach every template, layouts included.
//
// The one option html/template defines is missingkey, which says what happens when a map
// is indexed with a key it does not hold:
//
//	"missingkey=default" or "missingkey=invalid"
//		Do nothing and continue. A printed value becomes "<no value>".
//	"missingkey=zero"
//		Use the zero value for the map's element type.
//	"missingkey=error"
//		Stop execution with an error.
//
// It panics if an option string is not recognized.
func (v *Blocks) Option(opt ...string) *Blocks {
	// Validated here rather than at Load, so a typo panics at the call that made it, the
	// way the html/template Option does.
	template.New("blocks.option.check").Option(opt...)

	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	v.cfg.options = append(v.cfg.options, opt...)
	return v
}

// Funcs adds functions that every template can call, layouts included. The map is copied,
// so the caller may keep using it. Overwriting a name that is already there is allowed.
//
// The functions take effect at the next Load: templates that are already parsed keep the
// functions they were parsed with.
//
// It panics if a value is not a function with a return type html/template accepts.
func (v *Blocks) Funcs(funcMap template.FuncMap) *Blocks {
	if len(funcMap) == 0 {
		return v
	}
	validateFuncMapOrPanic(funcMap)

	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	if v.cfg.tmplFuncs == nil {
		v.cfg.tmplFuncs = make(template.FuncMap, len(funcMap))
	}
	maps.Copy(v.cfg.tmplFuncs, funcMap)
	return v
}

// AddFunc adds one function, the way Funcs adds a map. It takes effect at the next Load.
func (v *Blocks) AddFunc(funcName string, fn any) {
	v.Funcs(template.FuncMap{funcName: fn})
}

// LayoutFuncs adds functions that only layouts can call. The map is copied.
//
// A name added here can be overridden by Funcs, which is applied after it.
//
// It panics if a value is not a function with a return type html/template accepts.
func (v *Blocks) LayoutFuncs(funcMap template.FuncMap) *Blocks {
	if len(funcMap) == 0 {
		return v
	}
	validateFuncMapOrPanic(funcMap)

	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	if v.cfg.layoutFuncs == nil {
		v.cfg.layoutFuncs = make(template.FuncMap, len(funcMap))
	}
	maps.Copy(v.cfg.layoutFuncs, funcMap)
	return v
}

// validateFuncMapOrPanic keeps Funcs and LayoutFuncs panicking on a bad value the way
// html/template does, instead of failing much later inside Load.
func validateFuncMapOrPanic(funcMap template.FuncMap) {
	template.New("blocks.funcs.check").Funcs(funcMap)
}

// Ext returns the template file extension, with its dot.
func (v *Blocks) Ext() string {
	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	return v.cfg.extension
}

// Extension sets the template file extension, with its dot. It defaults to ".html".
func (v *Blocks) Extension(ext string) *Blocks {
	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	v.cfg.extension = ext
	return v
}

// Extensions registers a parser that runs on a file's contents before they are parsed as
// a template. The extension starts with a dot, such as ".md".
//
// One entry is registered by default, ".md", which renders markdown to HTML. Pass a nil
// parser to drop an extension, after which files carrying it are ignored unless it is the
// engine's own extension.
func (v *Blocks) Extensions(ext string, parser ExtensionParser) *Blocks {
	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	if v.cfg.extensionHandler == nil {
		v.cfg.extensionHandler = make(map[string]ExtensionParser, 1)
	}
	v.cfg.extensionHandler[ext] = parser
	return v
}

// RootDir sets the directory inside the file system that holds the templates. Everything
// outside it is ignored, and template names are relative to it.
//
// The directory is a slash separated path. A leading slash, a leading "./" and a trailing
// slash are all accepted, so "views", "/views", "./views" and "views/" name the same
// directory. An empty value, "/" or "." means the whole file system.
//
// It panics if the path cannot name a directory, such as one that climbs out of the file
// system with "..".
func (v *Blocks) RootDir(root string) *Blocks {
	root = path.Clean(strings.Trim(filepath.ToSlash(root), "/"))

	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	if v.cfg.fsys == nil || root == "" || root == "." {
		return v
	}

	sub, err := fs.Sub(v.cfg.fsys, root)
	if err != nil {
		panic(fmt.Errorf("blocks: RootDir: %w", err))
	}

	v.cfg.fsys = sub
	return v
}

// LayoutDir sets the directory that holds the layouts, relative to the root one. It
// defaults to "layouts".
//
// The directory is trimmed from a layout's name, so the file "layouts/main.html" is the
// layout "main". Naming it in full works too.
func (v *Blocks) LayoutDir(relToDirLayoutDir string) *Blocks {
	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	v.cfg.layoutDir = strings.Trim(filepath.ToSlash(relToDirLayoutDir), "/")
	return v
}

// DefaultLayout sets the layout ExecuteTemplate uses when it is given an empty one.
func (v *Blocks) DefaultLayout(layoutName string) *Blocks {
	v.cfgMu.Lock()
	defer v.cfgMu.Unlock()

	v.cfg.defaultLayoutName = layoutName
	return v
}

// Load parses the templates, including the layouts, into the engine. Call it once after
// configuring the engine and before rendering anything.
//
// If it fails, the templates from the previous successful Load keep working.
func (v *Blocks) Load() error {
	return v.LoadWithContext(context.Background())
}

// LoadWithContext is Load with a context, for a file system slow enough to be worth
// cancelling: a network mount, or a very large directory.
func (v *Blocks) LoadWithContext(ctx context.Context) error {
	v.loadMu.Lock()
	defer v.loadMu.Unlock()

	return v.buildLocked(ctx)
}

// buildLocked runs one build and records its result. Callers hold loadMu.
func (v *Blocks) buildLocked(ctx context.Context) error {
	err := v.build(ctx)

	v.lastErr = err
	v.loadCount++
	v.loadCountAtomic.Store(v.loadCount)
	return err
}

// reloadOnce rebuilds for Reload mode, collapsing a burst of concurrent requests into one
// build: a request that waited for loadMu while another request rebuilt takes that
// build's result rather than parsing everything a second time.
func (v *Blocks) reloadOnce() error {
	arrived := v.loadCountAtomic.Load()

	v.loadMu.Lock()
	defer v.loadMu.Unlock()

	if v.loadCount != arrived {
		return v.lastErr
	}

	return v.buildLocked(context.Background())
}

// build parses everything and, if that succeeds, publishes the result.
func (v *Blocks) build(ctx context.Context) error {
	v.cfgMu.Lock()
	cfg := v.cfg.clone()
	v.cfgMu.Unlock()

	files, err := readFiles(ctx, cfg.fsys)
	if err != nil {
		return err
	}

	contents, layouts, err := classify(cfg, files)
	if err != nil {
		return err
	}

	if len(contents) == 0 && len(layouts) == 0 {
		return ErrNoTemplates
	}

	s := &snapshot{
		templates: make(map[string]*template.Template, len(contents)),
		layouts:   make(map[layoutKey]*template.Template, len(layouts)*len(contents)),
		extension: cfg.extension,
		layoutDir: cfg.layoutDir,
	}

	// The functions are resolved now rather than at New, so a Register call made after
	// New reaches content templates and layouts alike.
	//
	// Precedence, weakest first: the registered functions, then any the file system
	// carries, then LayoutFuncs for layouts only, then Funcs and AddFunc.
	contentFuncs := translateFuncs(v, s, registeredFuncs())
	maps.Copy(contentFuncs, cfg.fsFuncs())

	layoutFuncs := maps.Clone(contentFuncs)
	maps.Copy(layoutFuncs, cfg.layoutFuncs)
	maps.Copy(layoutFuncs, cfg.tmplFuncs)
	maps.Copy(contentFuncs, cfg.tmplFuncs)

	root := template.New("root").Delims(cfg.left, cfg.right).Option(cfg.options...).Funcs(contentFuncs)
	if _, err = root.Parse(rootDefinition); err != nil {
		return err
	}

	for name, file := range contents {
		tmpl, err := root.Clone()
		if err != nil {
			return err
		}

		if _, err = tmpl.Parse(file.contents); err != nil {
			return &LoadError{File: file.path, Err: err}
		}

		s.templates[name] = tmpl
	}

	for layoutName, layoutFile := range layouts {
		// The layout is parsed once. This template is never executed, only cloned, which
		// is what lets one copy per content template cost a clone instead of a parse.
		base := template.New(layoutName).Delims(cfg.left, cfg.right).Option(cfg.options...).Funcs(layoutFuncs)
		if _, err = base.Parse(layoutFile.contents); err != nil {
			return &LoadError{File: layoutFile.path, Err: err}
		}

		for name, file := range contents {
			tmpl, err := base.Clone()
			if err != nil {
				return err
			}

			if _, err = tmpl.Parse(file.contents); err != nil {
				return &LoadError{File: file.path, Err: err}
			}

			s.layouts[layoutKey{layout: layoutName, name: name}] = tmpl
		}
	}

	if err = ctx.Err(); err != nil {
		return err
	}

	s.names = slices.Sorted(maps.Keys(s.templates))
	s.layoutNames = slices.Sorted(maps.Keys(layouts))

	v.snap.Store(s)
	return nil
}

// templateFile is one file's contents together with the path it came from, which is what
// an error names.
type templateFile struct {
	path     string
	contents string
}

// classify decides which files are layouts and which are content, and prepares the
// contents of each for parsing.
func classify(cfg config, files map[string][]byte) (contents, layouts map[string]templateFile, err error) {
	contents = make(map[string]templateFile, len(files))
	layouts = make(map[string]templateFile)
	pat := patternsFor(cfg.left, cfg.right)

	for filename, data := range files {
		ext := path.Ext(filename)
		if parser, ok := cfg.extensionHandler[ext]; ok && parser != nil {
			if data, err = parser(data); err != nil {
				return nil, nil, &LoadError{File: filename, Err: err}
			}
		} else if ext != cfg.extension {
			continue // not a template, and nothing knows how to convert it.
		}

		text := removeComments(string(data))

		name := strings.TrimPrefix(filename, "/")
		name = strings.TrimSuffix(name, cfg.extension)

		if pat.layout.MatchString(text) {
			text = pat.replaceYield(text)
			layouts[trimDir(name, cfg.layoutDir)] = templateFile{path: filename, contents: text}
			continue
		}

		contents[name] = templateFile{path: filename, contents: pat.wrapContent(text)}
	}

	return contents, layouts, nil
}

// ExecuteTemplate renders the template named tmplName into w, inside the layout named
// layoutName, with data as the template's dot.
//
// An empty layoutName falls back to the one given to DefaultLayout, and when that is
// empty too the template renders on its own. Either name may be given with or without the
// file extension, and a layout may be named with or without its directory.
//
// The error is an ErrNotExist when a name matches nothing loaded. Output may already have
// been written to w when an execution error comes back.
//
// It is safe to call from many goroutines. If they share a w, their output interleaves.
func (v *Blocks) ExecuteTemplate(w io.Writer, tmplName, layoutName string, data any) error {
	if v.reload.Load() {
		if err := v.reloadOnce(); err != nil {
			return err
		}
	}

	s := v.snap.Load()
	if s == nil {
		return ErrNotExist{Name: tmplName, Layout: layoutName}
	}

	if layoutName == "" {
		v.cfgMu.Lock()
		layoutName = v.cfg.defaultLayoutName
		v.cfgMu.Unlock()
	}

	return v.executeIn(s, w, tmplName, layoutName, data)
}

// executeIn renders inside one snapshot. A render, and every partial it calls, stays on
// the snapshot it started with.
func (v *Blocks) executeIn(s *snapshot, w io.Writer, tmplName, layoutName string, data any) error {
	name := s.templateName(tmplName)

	if layoutName != "" {
		layout := s.layoutName(layoutName)

		tmpl, ok := s.layouts[layoutKey{layout: layout, name: name}]
		if !ok {
			if !slices.Contains(s.layoutNames, layout) {
				return ErrNotExist{Name: name, Layout: layout, IsLayout: true}
			}
			return ErrNotExist{Name: name, Layout: layout}
		}

		return tmpl.Execute(w, data)
	}

	tmpl, ok := s.templates[name]
	if !ok {
		return ErrNotExist{Name: name}
	}

	return tmpl.Execute(w, data)
}

// templateName trims the extension a caller may have included.
func (s *snapshot) templateName(name string) string {
	return strings.TrimSuffix(name, s.extension)
}

// layoutName trims the extension and the layout directory, so "layouts/main.html",
// "layouts/main" and "main" all name the same layout.
func (s *snapshot) layoutName(name string) string {
	name = strings.TrimSuffix(name, s.extension)
	name = trimDir(name, s.layoutDir)
	return strings.TrimPrefix(name, "/")
}

// TemplateString renders a template and returns the result, the way ExecuteTemplate
// renders it into a writer.
//
// It does not reload in Reload mode. Call Load first when the files may have changed.
func (v *Blocks) TemplateString(tmplName, layoutName string, data any) (string, error) {
	s := v.snap.Load()
	if s == nil {
		return "", ErrNotExist{Name: tmplName, Layout: layoutName}
	}

	return v.templateStringIn(s, tmplName, layoutName, data)
}

func (v *Blocks) templateStringIn(s *snapshot, tmplName, layoutName string, data any) (string, error) {
	buf, _ := v.bufferPool.Get().(*bytes.Buffer)
	if buf == nil {
		buf = new(bytes.Buffer)
	}
	buf.Reset()

	err := v.executeIn(s, buf, tmplName, layoutName, data)
	contents := buf.String()
	v.bufferPool.Put(buf)
	return contents, err
}

// PartialFunc renders a template's content block and returns it as HTML, which is what
// the partial template function calls.
func (v *Blocks) PartialFunc(partialName string, data any) (template.HTML, error) {
	s := v.snap.Load()
	if s == nil {
		return "", ErrNotExist{Name: partialName}
	}

	return v.partialIn(s)(partialName, data)
}

// partialIn returns the partial function bound to one snapshot, so a partial rendered in
// the middle of a page uses the same templates as the page around it even when a reload
// lands between the two.
func (v *Blocks) partialIn(s *snapshot) func(string, any) (template.HTML, error) {
	return func(partialName string, data any) (template.HTML, error) {
		contents, err := v.templateStringIn(s, partialName, "", data)
		if err != nil {
			return "", err
		}
		return template.HTML(contents), nil
	}
}

// Lookup returns the parsed template for a name, or nil when there is none. Give an empty
// layoutName for the template on its own. Names are normalized the way ExecuteTemplate
// normalizes them.
//
// The returned template is the live one. Read it and execute it, but do not reparse or
// reconfigure it: it is shared with every other render, and the next Load replaces it.
func (v *Blocks) Lookup(tmplName, layoutName string) *template.Template {
	s := v.snap.Load()
	if s == nil {
		return nil
	}

	name := s.templateName(tmplName)
	if layoutName == "" {
		return s.templates[name]
	}

	return s.layouts[layoutKey{layout: s.layoutName(layoutName), name: name}]
}

// TemplateNames iterates the loaded template names in order.
func (v *Blocks) TemplateNames() iter.Seq[string] {
	s := v.snap.Load()
	if s == nil {
		return slices.Values([]string(nil))
	}
	return slices.Values(s.names)
}

// LayoutNames iterates the loaded layout names in order.
func (v *Blocks) LayoutNames() iter.Seq[string] {
	s := v.snap.Load()
	if s == nil {
		return slices.Values([]string(nil))
	}
	return slices.Values(s.layoutNames)
}

// ContextKeyType is the type of the request context value that carries an engine.
//
// See Set and Get.
type ContextKeyType struct{}

// ContextKey is the request context key an engine is stored under.
//
// See Set and Get.
var ContextKey ContextKeyType

// Set returns middleware that puts this engine in the request's context, for a program
// that serves different groups of routes from different engines.
//
// See Get.
func Set(v *Blocks) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(context.WithValue(r.Context(), ContextKey, v))
			next.ServeHTTP(w, r)
		})
	}
}

// Get returns the engine Set put in the request's context, or nil when there is none.
func Get(r *http.Request) *Blocks {
	v, _ := r.Context().Value(ContextKey).(*Blocks)
	return v
}

// trimDir removes a leading directory from a template name.
func trimDir(s string, dir string) string {
	dir = strings.Trim(dir, "/")
	if dir == "" || dir == "." {
		return s
	}

	return strings.TrimPrefix(s, dir+"/")
}

// matchHTMLCommentsRegex matches an HTML comment, including one that spans lines.
var matchHTMLCommentsRegex = regexp.MustCompile(`<!--[\s\S]*?-->`)

// removeComments strips HTML comments before parsing. It has to happen: the template
// parser reads a define inside a comment as a real one.
func removeComments(str string) string {
	return matchHTMLCommentsRegex.ReplaceAllString(str, "")
}
