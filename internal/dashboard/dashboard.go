// Package dashboard serves the single-page htmx UI for monitoring the queue.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var static embed.FS

// Handler returns an http.Handler that serves the dashboard at its mount point.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // impossible: embedded path is constant
	}
	return http.FileServer(http.FS(sub))
}
