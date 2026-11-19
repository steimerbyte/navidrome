package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The downloader is embedded in an iframe on Navidrome's own origin, so a DENY
// frame policy would render it as a blank box. Regression guard for that.
func TestDownloaderProxyFrameHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An upstream policy of its own must not survive either: two values
		// would both have to pass.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>downloader</h1>"))
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	assert.NoError(t, err)
	handler := newDownloaderProxy(target)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/downloader/", nil)
	// Emulate the secure middleware having already denied framing.
	rec.Header().Set("X-Frame-Options", "DENY")

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "downloader", rec.Body.String()[4:14])
	assert.Equal(t, []string{"SAMEORIGIN"}, rec.Header().Values("X-Frame-Options"))
}

func TestDownloaderProxyUnreachableUpstream(t *testing.T) {
	target, err := url.Parse("http://127.0.0.1:1")
	assert.NoError(t, err)

	rec := httptest.NewRecorder()
	newDownloaderProxy(target).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloader/", nil))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
