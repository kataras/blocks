package main

import (
	"embed"
	"net/http"
	"time"

	"github.com/kataras/blocks"
)

//go:embed data/*
var embeddedFS embed.FS

// System files are not used, you can optionally delete the folder and run the example now.
var views = blocks.New(embeddedFS).
	RootDir("data/views").
	Reload(true).
	LayoutDir("layouts").
	Funcs(map[string]any{
		"year": func() int {
			return time.Now().Year()
		},
	})

func main() {
	err := views.Load()
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/", index)
	http.HandleFunc("/500", internalServerError)

	println("Now listening on: http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]any{
		"Title": "Page Title",
	}

	err := views.ExecuteTemplate(w, "index", "main", data)
	if err != nil {
		println(err.Error())
	}
}

func internalServerError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)

	data := map[string]any{
		"Code":    http.StatusInternalServerError,
		"Message": "Internal Server Error",
	}
	views.ExecuteTemplate(w, "500", "error", data)
}
