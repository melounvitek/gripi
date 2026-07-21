package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/melounvitek/gripi/internal/config"
)

func NewHandler(_ config.Config, files fs.FS) (http.Handler, error) {
	public, err := fs.Sub(files, "public")
	if err != nil {
		return nil, fmt.Errorf("open embedded public files: %w", err)
	}

	assets := filesOnly(public, http.StripPrefix("/", http.FileServerFS(public)))
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", noCache(assets))
	mux.Handle("GET /apple-touch-icon.png", noCache(assets))
	return mux, nil
}

func filesOnly(root fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		file, err := fs.Stat(root, strings.TrimPrefix(request.URL.Path, "/"))
		if err != nil || file.IsDir() {
			http.NotFound(response, request)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(response, request)
	})
}
