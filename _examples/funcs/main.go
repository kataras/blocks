package main

import (
	"html/template"
	"net/http"

	"github.com/kataras/blocks"
	_ "github.com/kataras/blocks/_examples/funcs/mycollection"
)

var funcs = template.FuncMap{
	"add": func(i, j int) int {
		return i + j
	},
}

func main() {
	views := blocks.New("./views")
	views.Funcs(funcs)

	if err := views.Load(); err != nil {
		panic(err)
	}

	http.HandleFunc("/", calc(views))

	println("Now listening on: http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

func calc(views *blocks.Blocks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		err := views.ExecuteTemplate(w, "calc", "", nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

	}
}
