package nativeapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/utils"
)

// Extensions the scanner is expected to understand. Others are rejected instead of
// being written into the library.
var uploadAudioExtensions = map[string]bool{
	".aac": true, ".aif": true, ".aiff": true, ".ape": true, ".flac": true,
	".m4a": true, ".m4b": true, ".mp3": true, ".mpc": true, ".oga": true,
	".ogg": true, ".opus": true, ".wav": true, ".wma": true, ".wv": true,
}

var errUploadTooLarge = errors.New("file exceeds the configured limit")

func (api *Router) addUploadRoute(r chi.Router) {
	r.Post("/upload", api.handleUpload)
}

func maxUploadFileSize() int64 {
	size, err := humanize.ParseBytes(conf.Server().Uploads.MaxFileSize)
	if err != nil || size == 0 {
		size, _ = humanize.ParseBytes(consts.DefaultMaxUploadFileSize)
	}
	return int64(size)
}

// handleUpload stores one audio file into Uploads.Folder and triggers a scan.
// One file per request: the UI loops over the selection.
func (api *Router) handleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !conf.Server().Uploads.Enabled {
		http.Error(w, "file uploads are disabled (set Uploads.Enabled=true)", http.StatusForbidden)
		return
	}
	dir := strings.TrimSpace(conf.Server().Uploads.Folder)
	if dir == "" {
		http.Error(w, "Uploads.Folder is not configured", http.StatusInternalServerError)
		return
	}

	maxSize := maxUploadFileSize()
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+1<<20) // multipart envelope overhead
	if err := r.ParseMultipartForm(min(maxSize, 10<<20)); err != nil {
		log.Warn(ctx, "Upload: could not parse multipart form", err)
		http.Error(w, "invalid multipart form (file too large?)", http.StatusRequestEntityTooLarge)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `missing "file" field`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	name, err := uploadFileName(header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Error(ctx, "Upload: could not create upload folder", "file", dir, err)
		http.Error(w, "could not create upload folder", http.StatusInternalServerError)
		return
	}

	saved, err := writeUpload(dir, name, file, maxSize)
	if errors.Is(err, errUploadTooLarge) {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		log.Error(ctx, "Upload: could not store file", "file", name, err)
		http.Error(w, "could not store file", http.StatusInternalServerError)
		return
	}

	// The scanner serializes concurrent runs, so a burst of uploads does not stack scans.
	scanCtx := context.WithoutCancel(ctx)
	go func() {
		if _, err := api.scanner.ScanAll(scanCtx, false); err != nil {
			log.Error(scanCtx, "Upload: scan after upload failed", "file", saved, err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"name":%q}`, filepath.Base(saved))
}

// uploadFileName reduces a client-supplied name to a safe base name with a known
// audio extension. It never returns a path.
func uploadFileName(raw string) (string, error) {
	name := filepath.Base(strings.ReplaceAll(raw, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == 0 {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(name, "."))
	if name == "" || name == "." {
		return "", errors.New("invalid file name")
	}
	ext := filepath.Ext(name)
	if !uploadAudioExtensions[strings.ToLower(ext)] {
		return "", fmt.Errorf("unsupported file type %q", ext)
	}
	base := strings.TrimSuffix(name, ext)
	if len(base) > 120 {
		base = base[:120]
	}
	return base + ext, nil
}

// writeUpload streams src into a temp file and renames it into place, so a failed
// transfer never leaves a half-written track in the library folder.
func writeUpload(dir, name string, src io.Reader, maxSize int64) (string, error) {
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		_ = os.Remove(tmpName)
		return "", err
	}
	// CreateTemp uses 0600; library files are expected to be readable like any other track.
	if err := tmp.Chmod(0o644); err != nil {
		return fail(err)
	}

	written, err := io.Copy(tmp, io.LimitReader(src, maxSize+1))
	if err == nil && written > maxSize {
		err = errUploadTooLarge
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}

	target := uniqueTarget(dir, name)
	if err := os.Rename(tmpName, target); err != nil {
		return fail(err)
	}
	return target, nil
}

// shortcut: existence check and rename are not atomic; a single admin uploading in
// parallel could lose a name race. Add O_TMPFILE/linkat if that ever matters.
func uniqueTarget(dir, name string) string {
	target := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; utils.FileExists(target); i++ {
		target = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	return target
}
