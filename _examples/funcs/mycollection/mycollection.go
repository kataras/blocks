// Package mycollection contains a template.FuncMap that should be registered
// across all Blocks view engines. Can be registered through _ "$your_package/$your_collection"
package mycollection

import (
	"html/template"

	"github.com/kataras/blocks"
)

var funcs = template.FuncMap{ // map[string]interface{}
	"sub": func(i, j int) int {
		return i - j
	},
}

func init() {
	blocks.Register(funcs)
}

// Note: as a special feature, the function input's can be a type of
// func(*Blocks) (fn interface{}) or func(*Blocks) template.FuncMap where you
// need access to the current Blocks view engine.
