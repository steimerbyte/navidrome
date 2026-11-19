package nativeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/core/ffmpeg"
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

const maxUploadBaseBytes = 120

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
	// Truncate on runes, not bytes: cutting a multi-byte character leaves a
	// name the filesystem stores but the scanner cannot read back as UTF-8.
	if len(base) > maxUploadBaseBytes {
		r := []rune(base)
		for len(string(r)) > maxUploadBaseBytes {
			r = r[:len(r)-1]
		}
		base = string(r)
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

	// Conversion runs on the complete temp file, before it becomes visible in the
	// library, so a failed conversion can never leave a half-written track behind.
	if convertTo := strings.ToLower(strings.TrimSpace(conf.Server().Uploads.ConvertTo)); convertTo == consts.UploadConvertMP3 {
		if filepath.Ext(name) != ".mp3" {
			// Assign to a separate variable: on error convertToMP3 returns "", and
			// tmpName must still point at the file fail() has to remove.
			converted, convErr := convertToMP3(tmpName)
			if convErr != nil {
				return fail(convErr)
			}
			tmpName = converted
			name = strings.TrimSuffix(name, filepath.Ext(name)) + ".mp3"
		}
	}

	target := uniqueTarget(dir, name)
	if err := os.Rename(tmpName, target); err != nil {
		return fail(err)
	}
	return target, nil
}

// uploadFFmpeg re-encodes src into dst, carrying tags over. It is a variable
// because ffmpeg.New() resolves and caches the binary in a sync.Once, which a
// test cannot reset.
var uploadFFmpeg = func(ffmpegPath, ffprobePath, src, dst string) ([]byte, error) {
	args := []string{"-v", "error", "-y", "-i", src,
		// Keep the audio and, if present, the cover art stream.
		"-map", "0:a:0", "-map", "0:v?", "-map_metadata", "0",
	}
	// -map_metadata only copies format-level tags. Ogg/Opus keeps them per stream,
	// so a plain conversion would silently drop artist/title/album.
	for _, tag := range probeTags(ffprobePath, src) {
		args = append(args, "-metadata", tag)
	}
	args = append(args,
		"-c:a", "libmp3lame", "-q:a", "0", "-c:v", "copy",
		"-id3v2_version", "3",
		"-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)",
		dst,
	)
	return exec.Command(ffmpegPath, args...).CombinedOutput()
}

// uploadFFmpegReal is the production runner, restored by tests.
var uploadFFmpegReal = uploadFFmpeg

// probeTags returns the source's format-level and first audio stream's tags as
// key=value arguments for ffmpeg. Values are reported by ffprobe, not by the client.
func probeTags(ffprobePath, src string) []string {
	// Two separate -show_entries flags: "format_tags:stream_tags" is parsed as a
	// file name by ffprobe, which then fails and would silently drop every tag.
	// codec_type must be requested explicitly, or the stream filter below matches
	// nothing and the audio tags are never read.
	out, err := exec.Command(ffprobePath, "-v", "error",
		"-show_entries", "format_tags", "-show_entries", "stream_tags",
		"-show_entries", "stream=codec_type",
		"-of", "json", src).Output()
	if err != nil {
		return nil
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecType string            `json:"codec_type"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &probe) != nil {
		return nil
	}
	seen := map[string]bool{}
	var tags []string
	add := func(k, v string) {
		k = strings.ToLower(k)
		// Drop encoder and container bookkeeping: they describe the old file.
		if v == "" || seen[k] || k == "encoder" || k == "language" {
			return
		}
		seen[k] = true
		tags = append(tags, k+"="+v)
	}
	for k, v := range probe.Format.Tags {
		add(k, v)
	}
	for _, s := range probe.Streams {
		if s.CodecType != "audio" {
			continue
		}
		for k, v := range s.Tags {
			add(k, v)
		}
		break
	}
	sort.Strings(tags) // deterministic argv, so tests can assert on it
	return tags
}

// convertToMP3 re-encodes src with libmp3lame at the highest VBR setting. The cover
// art stream is copied rather than re-encoded and the source tags are carried over,
// because Navidrome serves the embedded picture from the stored file.
func convertToMP3(src string) (string, error) {
	ffmpegPath, err := ffmpeg.New().CmdPath()
	if err != nil {
		return "", err
	}
	// ffprobe sits next to ffmpeg and is named after it (see core/ffmpeg).
	ffprobePath := strings.Replace(filepath.Base(ffmpegPath), "ffmpeg", "ffprobe", 1)
	dst := src + ".mp3"
	if out, err := uploadFFmpeg(ffmpegPath, filepath.Join(filepath.Dir(ffmpegPath), ffprobePath), src, dst); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("converting to mp3: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return dst, nil
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
