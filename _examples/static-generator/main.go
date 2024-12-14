package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kataras/blocks"
	"gopkg.in/yaml.v3"
)

const outputDir = "./public"

type data = map[string]any

var defaultFuncs = data{
	"year": func() int {
		return time.Now().Year()
	},
}

var siteData = data{
	"Title": "Default Title",
	// You can also have funcs here, with {{ .add 2 2}}:
	// "add": func(i,j int) int { return i+j },
}

func main() {
	if err := readConfig(siteData); err != nil {
		log.Fatalf("readConfig: %v", err)
	}

	for k, v := range siteData {
		fmt.Printf("%s=%v\n", k, v)
	}

	views := blocks.New("../basic/views").Funcs(defaultFuncs)
	if err := views.Load(); err != nil {
		log.Fatalf("views: %v", err)
	}

	// You can get information about templates and their layouts through:
	// for name := range views.Templates {
	// 	layouts := make([]string, 0, len(views.Layouts))
	// 	for layout := range views.Layouts {
	// 		layouts = append(layouts, layout)
	// 	}
	// 	fmt.Printf("[%s] with Layouts [%s]\n", name, strings.Join(layouts, ", "))
	// }

	err := os.MkdirAll(outputDir, 0777)
	if err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	indexPath := filepath.Join(outputDir, "index.html")
	indexFile, err := os.OpenFile(indexPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("open index file: %v", err)
	}

	err = views.ExecuteTemplate(indexFile, "index", "main", siteData)
	if err != nil {
		log.Fatalf("template exec: %v", err)
	}

	log.Printf("Generated: %s\n", indexPath)

	// let's serve our static content:
	log.Println("Now listening on: http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", http.FileServer(http.Dir("./public"))))
}

func readConfig(dest map[string]any) error {
	f, err := os.Open("./site.yml")
	if err != nil {
		return err
	}
	defer f.Close()

	return yaml.NewDecoder(f).Decode(dest)
}
