package nativeapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Upload API", func() {
	DescribeTable("uploadFileName",
		func(raw, want, wantErr string) {
			got, err := uploadFileName(raw)
			if wantErr != "" {
				Expect(err).To(MatchError(wantErr))
				return
			}
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("keeps unicode and case", "Sigur Rós - Hoppípolla.flac", "Sigur Rós - Hoppípolla.flac", ""),
		Entry("strips unix path components", "../../etc/passwd.mp3", "passwd.mp3", ""),
		Entry("strips windows path components", `C:\Music\a.MP3`, "a.MP3", ""),
		Entry("truncates a long base but keeps the extension", strings.Repeat("a", 200)+".flac", strings.Repeat("a", 120)+".flac", ""),
		Entry("rejects a bare extension", ".mp3", "", `unsupported file type ""`),
		Entry("rejects a name without extension", "noextension", "", `unsupported file type ""`),
		Entry("rejects an empty name", "", "", "invalid file name"),
		Entry("rejects an executable", "x.exe", "", `unsupported file type ".exe"`),
		Entry("rejects a text file", "x.txt", "", `unsupported file type ".txt"`),
	)

	It("truncates a multibyte name on rune boundaries", func() {
		got, err := uploadFileName(strings.Repeat("é", 200) + ".opus")
		Expect(err).ToNot(HaveOccurred())
		Expect(utf8.ValidString(got)).To(BeTrue())
		Expect(len(strings.TrimSuffix(got, ".opus"))).To(BeNumerically("<=", maxUploadBaseBytes))
	})

	DescribeTable("uniqueTarget",
		func(taken []string, want string) {
			dir := GinkgoT().TempDir()
			for _, name := range taken {
				Expect(os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)).To(Succeed())
			}
			Expect(uniqueTarget(dir, "name.flac")).To(Equal(filepath.Join(dir, want)))
		},
		Entry("keeps a free name", []string{}, "name.flac"),
		Entry("avoids one existing file", []string{"name.flac"}, "name (2).flac"),
		Entry("avoids two existing files", []string{"name.flac", "name (2).flac"}, "name (3).flac"),
	)

	Describe("writeUpload", func() {
		It("writes the bytes unmangled and leaves no temp file", func() {
			dir := GinkgoT().TempDir()
			content := []byte("ID3\x04\x00\x00\x00\x00\x00\x00\xff\xfe binary")
			saved, err := writeUpload(dir, "song.mp3", bytes.NewReader(content), 1024)
			Expect(err).ToNot(HaveOccurred())
			Expect(saved).To(Equal(filepath.Join(dir, "song.mp3")))
			got, err := os.ReadFile(saved)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(Equal(content))
			Expect(os.ReadDir(dir)).To(HaveLen(1))
			// A 0600 leftover of CreateTemp would leave the track unreadable in the library.
			st, err := os.Stat(saved)
			Expect(err).ToNot(HaveOccurred())
			Expect(st.Mode().Perm()).To(Equal(os.FileMode(0o644)))
		})

		It("refuses files over the limit and cleans up the temp file", func() {
			dir := GinkgoT().TempDir()
			saved, err := writeUpload(dir, "song.mp3", bytes.NewReader(make([]byte, 11)), 10)
			Expect(err).To(MatchError(errUploadTooLarge))
			Expect(saved).To(BeEmpty())
			Expect(os.ReadDir(dir)).To(BeEmpty())
		})
	})

	Describe("handleUpload", func() {
		var dir string
		var api *Router
		var scanner *tests.MockScanner

		// multipartForm builds a request with the given file part; name = "" writes a
		// field "notafile" instead, so the form parses but has no "file" part.
		multipartForm := func(name string, content []byte) *http.Request {
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			if name == "" {
				Expect(mw.WriteField("notafile", "x")).To(Succeed())
			} else {
				part, err := mw.CreateFormFile("file", name)
				Expect(err).ToNot(HaveOccurred())
				_, err = part.Write(content)
				Expect(err).ToNot(HaveOccurred())
			}
			Expect(mw.Close()).To(Succeed())
			req := httptest.NewRequest(http.MethodPost, "/upload", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			return req
		}

		BeforeEach(func() {
			dir = filepath.Join(GinkgoT().TempDir(), "uploads")
			scanner = tests.NewMockScanner()
			api = &Router{scanner: scanner}
			conf.Update(func(c *conf.Options) {
				c.Uploads.Enabled = true
				c.Uploads.Folder = dir
			})
		})

		It("is forbidden when uploads are disabled", func() {
			conf.Update(func(c *conf.Options) { c.Uploads.Enabled = false })
			w := httptest.NewRecorder()
			api.handleUpload(w, multipartForm("song.mp3", []byte("audio")))
			Expect(w.Code).To(Equal(http.StatusForbidden))
			Expect(dir).ToNot(BeADirectory())
		})

		It("rejects a request without a file part", func() {
			w := httptest.NewRecorder()
			api.handleUpload(w, multipartForm("", nil))
			Expect(w.Code).To(Equal(http.StatusBadRequest))
			Expect(dir).ToNot(BeADirectory())
		})

		It("rejects an unsupported extension without writing anything", func() {
			w := httptest.NewRecorder()
			api.handleUpload(w, multipartForm("virus.exe", []byte("MZ")))
			Expect(w.Code).To(Equal(http.StatusBadRequest))
			Expect(dir).ToNot(BeADirectory())
		})

		It("stores a valid file and triggers a scan", func() {
			w := httptest.NewRecorder()
			api.handleUpload(w, multipartForm("song.mp3", []byte("audio")))
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal(`{"name":"song.mp3"}`))
			got, err := os.ReadFile(filepath.Join(dir, "song.mp3"))
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(Equal([]byte("audio")))
			Eventually(scanner.GetScanAllCallCount, "5s").Should(Equal(1))
		})

		It("rejects a file over the size limit", func() {
			conf.Update(func(c *conf.Options) { c.Uploads.MaxFileSize = "1KB" })
			w := httptest.NewRecorder()
			api.handleUpload(w, multipartForm("song.mp3", bytes.Repeat([]byte("a"), 2000)))
			Expect(w.Code).To(Equal(http.StatusRequestEntityTooLarge))
			Expect(os.ReadDir(dir)).To(BeEmpty())
		})
	})
})
