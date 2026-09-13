package blocks

import (
	"html/template"
	"maps"
	"sync"
)

// builtinsMu guards builtins, which any goroutine may write through Register and which
// every Load reads.
var builtinsMu sync.RWMutex

// builtins are the functions every engine starts with. The partial entry takes the
// snapshot being built as well as the engine, so that a partial called in the middle of a
// render resolves against the same templates as the page around it.
var builtins = template.FuncMap{
	"partial": func(v *Blocks, s *snapshot) any {
		return v.partialIn(s)
	},
}

// Register adds functions that every engine can use, the way database/sql.Register adds a
// driver. Call it from an init function, before the engines are created.
//
// A value is usually a plain function, as html/template wants. Two other forms are
// accepted, for a function that needs the engine it belongs to:
//
//	func(*Blocks) any              // returns the function to register
//	func(*Blocks) template.FuncMap // returns a whole map, resolved the same way
//
// Overwriting a name that is already registered is allowed, including the built-in
// partial. The functions are picked up by the next Load of each engine.
//
// Usage:
//
//	package myfuncs
//
//	func init() {
//		blocks.Register(template.FuncMap{"year": func() int { return time.Now().Year() }})
//	}
//
// The program then imports that package for its side effect, and every engine has the
// function:
//
//	import _ "myfuncs"
func Register(funcMap template.FuncMap) {
	builtinsMu.Lock()
	defer builtinsMu.Unlock()

	maps.Copy(builtins, funcMap)
}

// registeredFuncs returns a copy of the registered functions, taken at the start of a
// build so that a Register call running at the same time cannot change it mid-parse.
func registeredFuncs() template.FuncMap {
	builtinsMu.RLock()
	defer builtinsMu.RUnlock()

	return maps.Clone(builtins)
}

// translateFuncs resolves the engine-aware forms described on Register into plain
// functions that html/template can call.
func translateFuncs(v *Blocks, s *snapshot, funcMap template.FuncMap) template.FuncMap {
	funcs := make(template.FuncMap, len(funcMap))

	for name, fn := range funcMap {
		if fn == nil {
			continue
		}

		switch f := fn.(type) {
		case func(*Blocks) any:
			funcs[name] = f(v)
		case func(*Blocks, *snapshot) any:
			funcs[name] = f(v, s)
		case func(*Blocks) template.FuncMap:
			maps.Copy(funcs, translateFuncs(v, s, f(v)))
		default:
			funcs[name] = fn
		}
	}

	return funcs
}
