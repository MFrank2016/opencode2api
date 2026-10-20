package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"opencode2api/webui"
)

// asset is one embedded WebUI file: the bytes to serve, the type to label
// them with, and a strong ETag derived from the bytes themselves.
type asset struct {
	body        []byte
	contentType string
	etag        string
}

// assetHandler serves the embedded WebUI. index.html answers the root and
// every path that is not an embedded file, so the single-page app can route
// the rest client-side.
//
// Every response carries a strong ETag and Cache-Control: no-cache. Embedded
// files have no modification time, so without a validator a browser or CDN
// keeps the previous build's CSS and JavaScript after an upgrade; a content
// hash makes that revalidation cheap (304) and exact.
type assetHandler struct {
	files map[string]asset
}

// newAssetHandler indexes the embedded assets once, at startup, so serving a
// request only copies bytes and compares an ETag.
func newAssetHandler(assets fs.FS) (*assetHandler, error) {
	handler := &assetHandler{files: make(map[string]asset)}
	err := fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		handler.files["/"+name] = asset{
			body:        body,
			contentType: contentType(name),
			etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	index, ok := handler.files["/index.html"]
	if !ok {
		return nil, errors.New("webui assets have no index.html")
	}
	handler.files["/"] = index
	return handler, nil
}

func (h *assetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeAdminError(w, http.StatusNotFound, "not_found", "management endpoint not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeAdminError(w, http.StatusMethodNotAllowed, "method_not_allowed", "the WebUI is read-only")
		return
	}
	file, ok := h.files[r.URL.Path]
	if !ok {
		file = h.files["/"]
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", file.contentType)
	w.Header().Set("ETag", file.etag)
	if etagMatches(r.Header.Get("If-None-Match"), file.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(file.body)))
	if r.Method == http.MethodHead {
		return
	}
	// The client may have disconnected mid-response; there is nothing useful
	// left to do with that error.
	_, _ = w.Write(file.body)
}

// staticHandler serves the embedded WebUI. The assets are compiled into the
// binary, so a read failure means a broken build; log it and fall back to the
// plain file server rather than taking the console down.
func (a *Server) staticHandler() http.Handler {
	handler, err := newAssetHandler(webui.Assets)
	if err != nil {
		a.logger.Error("webui assets unavailable", "component", "admin", "event", "assets_unavailable", "error", err)
		return http.FileServer(http.FS(webui.Assets))
	}
	return handler
}

// contentType labels an embedded file from its extension. The three types the
// console ships are pinned so the value cannot depend on the host's
// /etc/mime.types; X-Content-Type-Options: nosniff would turn a wrong label
// into a refused stylesheet or module.
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	}
	if value := mime.TypeByExtension(path.Ext(name)); value != "" {
		return value
	}
	return "application/octet-stream"
}

// etagMatches reports whether an If-None-Match header covers etag. The header
// is a comma-separated list that may use weak validators or "*".
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
