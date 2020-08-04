package main

import (
	"net/http"

	"github.com/kataras/blocks"
)

// $ go get -u github.com/go-bindata/go-bindata/v3/go-bindata
// $ go-bindata -prefix "../basic" ../basic/views/...
// $ go run .
// # OR go-bindata -prefix "../basic/views" ../basic/views/... with blocks.New("").Assets(...)
// System files are not used, you can optionally delete the folder and run the example now.
var views = blocks.New("./views").Assets(Asset, AssetNames)

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

	data := map[string]interface{}{
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

	data := map[string]interface{}{
		"Code":    http.StatusInternalServerError,
		"Message": "Internal Server Error",
	}
	views.ExecuteTemplate(w, "500", "error", data)
}
