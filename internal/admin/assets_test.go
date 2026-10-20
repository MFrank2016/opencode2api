package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/webui"
)

func newTestAssetHandler(t *testing.T) *assetHandler {
	t.Helper()
	handler, err := newAssetHandler(webui.Assets)
	if err != nil {
		t.Fatalf("newAssetHandler: %v", err)
	}
	return handler
}

// The console is a single-page app: the root and every unknown path must
// answer with index.html so client-side hash routing works on a fresh load.
func TestAssetHandlerServesIndexForRootAndUnknownPaths(t *testing.T) {
	handler := newTestAssetHandler(t)
	want, err := webui.Assets.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	for _, target := range []string{"/", "/index.html", "/overview"} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

			response := recorder.Result()
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			if string(body) != string(want) {
				t.Fatal("body is not index.html")
			}
			if got := response.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type = %q, want text/html; charset=utf-8", got)
			}
		})
	}
}

// A cached console must revalidate on every load: the build replaces the
// embedded assets in place, so a browser or CDN that keeps the previous CSS
// and JavaScript renders the previous interface after an upgrade.
func TestAssetHandlerRequiresRevalidation(t *testing.T) {
	handler := newTestAssetHandler(t)
	for _, target := range []string{"/", "/css/base.css", "/js/main.js"} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

			if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("Cache-Control = %q, want no-cache", got)
			}
			if recorder.Header().Get("ETag") == "" {
				t.Fatal("ETag is empty; a revalidating cache needs a validator")
			}
		})
	}
}

func TestAssetHandlerAnswersMatchingETagWithNotModified(t *testing.T) {
	handler := newTestAssetHandler(t)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/css/base.css", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is empty")
	}

	request := httptest.NewRequest(http.MethodGet, "/css/base.css", nil)
	request.Header.Set("If-None-Match", etag)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", recorder.Body.String())
	}
}

func TestAssetHandlerServesEmbeddedAssetsWithTheirType(t *testing.T) {
	handler := newTestAssetHandler(t)
	for target, wantType := range map[string]string{
		"/index.html":   "text/html; charset=utf-8",
		"/css/base.css": "text/css; charset=utf-8",
		"/js/main.js":   "text/javascript; charset=utf-8",
	} {
		t.Run(target, func(t *testing.T) {
			want, err := webui.Assets.ReadFile(strings.TrimPrefix(target, "/"))
			if err != nil {
				t.Fatalf("read embedded %s: %v", target, err)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

			response := recorder.Result()
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			if string(body) != string(want) {
				t.Fatalf("body is not the embedded %s", target)
			}
			if got := response.Header.Get("Content-Type"); got != wantType {
				t.Fatalf("Content-Type = %q, want %q", got, wantType)
			}
		})
	}
}

func TestAssetHandlerRejectsUnknownAPIPath(t *testing.T) {
	handler := newTestAssetHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/missing", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestAssetHandlerRejectsNonReadMethods(t *testing.T) {
	handler := newTestAssetHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q, want GET, HEAD", got)
	}
}

func TestAssetHandlerServesHeadWithoutBody(t *testing.T) {
	handler := newTestAssetHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, "/js/main.js", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", recorder.Body.String())
	}
	if recorder.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD response has no Content-Length")
	}
}
