package main

import (
	"net/http"

	"github.com/kataras/blocks"
)

var views = blocks.New("./views/public")

func main() {
	err := views.Load()
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/", index)

	// Create a new Blocks set for the "admin".
	adminViews := blocks.New("./views/admin")
	if err = adminViews.Load(); err != nil {
		panic(err)
	}

	// Two methods:
	// 1. Wrapping the handler manually:
	http.HandleFunc("/admin", admin(adminViews))
	// 2. Using the `Set` middleware:
	middleware := blocks.Set(adminViews)
	http.Handle("/admin2", middleware(admin2{
		Title: "Admin 2 Panel",
	}))

	println("Now listening on: http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

func index(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title": "Page Title",
	}

	views.ExecuteTemplate(w, "index", "main", data)
}

func admin(v *blocks.Blocks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := map[string]interface{}{
			"Title": "Admin Panel",
		}
		err := v.ExecuteTemplate(w, "index", "main", data)
		if err != nil {
			panic(err)
		}
	}
}

type admin2 struct {
	Title string
}

func (h admin2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v := blocks.Get(r)
	v.ExecuteTemplate(w, "index", "main", h)
}
