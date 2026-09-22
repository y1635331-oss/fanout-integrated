package main

import (
	"embed"
	"net/http"
)

//go:embed pool.html
var poolAssets embed.FS

func handlePoolPage(w http.ResponseWriter, r *http.Request) {
	b, e := poolAssets.ReadFile("pool.html")
	if e != nil {
		http.Error(w, "页面不可用", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}
