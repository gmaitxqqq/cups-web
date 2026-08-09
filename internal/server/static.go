package server

import (
    "bytes"
    "embed"
    "io"
    "io/fs"
    "net/http"
    "path"
    "time"
)

// NewEmbeddedServer returns an http.Handler that serves embedded frontend files
// and falls back to index.html for SPA routes.
func NewEmbeddedServer(efs embed.FS) http.Handler {
    sub, err := fs.Sub(efs, "dist")
    if err != nil {
        // fallback to root
        sub, _ = fs.Sub(efs, "")
    }
    fileServer := http.FileServer(http.FS(sub))

    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // normalize path and remove leading '/'
        p := path.Clean(r.URL.Path)
        if p == "/" {
            p = "index.html"
        } else {
            p = p[1:]
        }
        // attempt to stat the file in the embedded fs
        if _, err := fs.Stat(sub, p); err == nil {
            fileServer.ServeHTTP(w, r)
            return
        }
        // fallback to index.html
        f, err := sub.Open("index.html")
        if err != nil {
            http.NotFound(w, r)
            return
        }
        defer f.Close()

        // Read file bytes and serve using a ReadSeeker (bytes.Reader)
        data, err := io.ReadAll(f)
        if err != nil {
            http.Error(w, "failed to read embedded index.html", http.StatusInternalServerError)
            return
        }

        modTime := time.Now()
        if fi, err := f.Stat(); err == nil {
            modTime = fi.ModTime()
        }

        http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(data))
    })
}
