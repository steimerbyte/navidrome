# Music uploads (fork addition)

Admin-only file upload into the library, so new music can be added from the web UI
instead of SCP/rsync. Uploads are written to one folder and indexed by the normal
scanner; the fork changes nothing about how Navidrome stores or tags files.

## Configuration

```toml
[Uploads]
Enabled = true
Folder = "/music/Inbox"
MaxFileSize = "500MB"
```

| Key | Env var | Default | Meaning |
|---|---|---|---|
| `Uploads.Enabled` | `ND_UPLOADS_ENABLED` | `false` | Enables the endpoint and the UI page |
| `Uploads.Folder` | `ND_UPLOADS_FOLDER` | — | Target directory; required when enabled |
| `Uploads.MaxFileSize` | `ND_UPLOADS_MAXFILESIZE` | `500MB` | Per-file limit, parsed by go-humanize |

`Uploads.Folder` must be inside (or be) a configured music folder, otherwise the
scanner will never see the files. The folder is created on first upload.

## Behaviour

- `POST /api/upload`, admin JWT, multipart field `file`, one file per request.
- `200 {"name":"…"}` on success; `400` unknown extension or unusable name;
  `403` uploads disabled or non-admin; `413` file above `MaxFileSize`;
  `500` write failure.
- Allowed extensions: `.aac .aif .aiff .ape .flac .m4a .m4b .mp3 .mpc .oga .ogg
  .opus .wav .wma .wv`. Everything else is rejected, not written.
- Names are reduced to their base name (no path separators, no control
  characters); an existing file is never overwritten, the new one becomes
  `name (2).ext`, `name (3).ext`, …
- Files are streamed to a temp file in the target folder, fsynced, then renamed
  into place, so a failed transfer leaves no half-written track.
- Every accepted file triggers one background library scan (the scanner
  serialises concurrent runs).

## Manual check

```bash
TOKEN=$(curl -s -X POST localhost:4533/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"…"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
curl -s -H "X-ND-Authorization: Bearer $TOKEN" -F "file=@track.flac" localhost:4533/api/upload
```

The UI page is at `/app/#/upload`, and only appears for admins while
`Uploads.Enabled` is true. Its menu entry sits in the user menu (avatar,
top right) next to Personal/Users/Transcoding, because a resource in the main
sidebar trips a react-admin v3 render crash at desktop width.

## Scope

Deliberately not implemented: chunked/resumable uploads, tag editing, library
picker, non-admin uploads, checksum dedupe. One request carries one file.
