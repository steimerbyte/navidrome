package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
)

// mountDownloaderProxy forwards /downloader/* to a downloader service running in
// the same container, so the UI can embed it in an iframe on the same origin.
func (s *Server) mountDownloaderProxy() {
	raw := strings.TrimSpace(conf.Server().Downloader.ProxyTarget)
	if raw == "" {
		return
	}
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" {
		log.Error("Invalid Downloader.ProxyTarget, proxy not mounted", "target", raw)
		return
	}
	handler := newDownloaderProxy(target)

	urlPath := path.Join(conf.Server().BasePath, "/downloader")
	s.router.Group(func(r chi.Router) {
		r.Use(middleware.RequestID, middleware.Recoverer, realIPMiddleware)
		r.Handle(urlPath, handler)
		r.Handle(urlPath+"/*", handler)
	})
	log.Info("Mounting downloader proxy", "path", urlPath, "target", raw)
}

func newDownloaderProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	// The downloader is embedded in an iframe on this origin, so it must not carry
	// a frame policy of its own: header copying appends, and two values would both
	// have to pass.
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("X-Frame-Options")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Warn(r.Context(), "Downloader proxy request failed", "url", r.URL.Path, err)
		http.Error(w, "downloader is not reachable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The default middleware chain sets X-Frame-Options: DENY before handlers
		// run, which would blank that same-origin iframe. Replace it before the
		// proxy writes the response.
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		proxy.ServeHTTP(w, r)
	})
}
