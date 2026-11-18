import { useRef, useState } from 'react'
import { Title, useNotify, useTranslate } from 'react-admin'
import { Button, Paper, Typography } from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import CloudUploadIcon from '@material-ui/icons/CloudUpload'
import clsx from 'clsx'
import { useDirectDataProvider } from '../dataProvider'
import { formatBytes } from '../utils'

// Mirrors the extension allowlist in server/nativeapi/upload.go: anything else would
// only be refused after the whole file has been sent.
const AUDIO_EXTENSIONS = new Set([
  'aac',
  'aif',
  'aiff',
  'ape',
  'flac',
  'm4a',
  'm4b',
  'mp3',
  'mpc',
  'oga',
  'ogg',
  'opus',
  'wav',
  'wma',
  'wv',
])

const ACCEPT =
  'audio/*,.flac,.mp3,.ogg,.opus,.m4a,.wav,.aiff,.wma,.ape,.wv,.mpc,.aac'

const STATUS_LABELS = {
  queued: 'Queued',
  uploading: 'Uploading',
  done: 'Done',
  error: 'Failed',
}

let lastId = 0

const extensionOf = (name) => {
  const dot = name.lastIndexOf('.')
  return dot < 0 ? '' : name.slice(dot + 1).toLowerCase()
}

// The handler writes errors with http.Error, whose plain-text body fetchJson drops
// on non-2xx; the reason phrase plus status code is what survives.
const errorMessage = (err) =>
  err.status ? `HTTP ${err.status}: ${err.message}` : err.message

const useStyles = makeStyles(
  (theme) => ({
    paper: {
      padding: theme.spacing(3),
      marginTop: theme.spacing(2),
    },
    dropZone: {
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      gap: theme.spacing(1),
      padding: theme.spacing(4),
      textAlign: 'center',
      border: `2px dashed ${theme.palette.divider}`,
      borderRadius: theme.shape.borderRadius,
    },
    dragging: {
      borderColor: theme.palette.primary.main,
      backgroundColor: theme.palette.action.hover,
    },
    icon: {
      fontSize: 48,
      color: theme.palette.text.secondary,
    },
    rows: {
      marginTop: theme.spacing(3),
    },
    row: {
      display: 'flex',
      alignItems: 'baseline',
      gap: theme.spacing(2),
      padding: theme.spacing(1, 0),
      borderBottom: `1px solid ${theme.palette.divider}`,
    },
    name: {
      flex: 1,
      overflowWrap: 'anywhere',
    },
    size: {
      color: theme.palette.text.secondary,
    },
    status: {
      minWidth: 90,
      textAlign: 'right',
    },
    error: {
      color: theme.palette.error.main,
    },
  }),
  { name: 'NDUpload' },
)

const UploadPage = () => {
  const classes = useStyles()
  const translate = useTranslate()
  const notify = useNotify()
  const dataProvider = useDirectDataProvider()
  const inputRef = useRef(null)
  const queueRef = useRef([])
  const drainingRef = useRef(false)
  const [dragging, setDragging] = useState(false)
  const [rows, setRows] = useState([])

  const updateRow = (id, patch) =>
    setRows((current) =>
      current.map((row) => (row.id === id ? { ...row, ...patch } : row)),
    )

  // One queue for the page, drained by two workers: the endpoint takes one file
  // per request, and a second drop while uploading must not double the workers.
  const drainQueue = async () => {
    if (drainingRef.current) return
    drainingRef.current = true
    let done = 0
    let failed = 0
    const worker = async () => {
      for (;;) {
        const item = queueRef.current.shift()
        if (!item) return
        updateRow(item.id, { status: 'uploading' })
        try {
          await dataProvider.uploadFile(item.file)
          updateRow(item.id, { status: 'done' })
          done++
        } catch (err) {
          updateRow(item.id, { status: 'error', error: errorMessage(err) })
          failed++
        }
      }
    }
    // Workers return only once the queue is empty, so one pass drains everything;
    // files dropped meanwhile are picked up by the running workers.
    await Promise.all([worker(), worker()])
    drainingRef.current = false
    notify('resources.upload.summary', {
      type: failed ? 'warning' : 'success',
      messageArgs: { done, failed },
    })
  }

  const addFiles = (files) => {
    const added = []
    const rejected = []
    Array.from(files).forEach((file) => {
      const row = { id: ++lastId, name: file.name, size: file.size }
      if (AUDIO_EXTENSIONS.has(extensionOf(file.name))) {
        added.push({ ...row, file, status: 'queued' })
      } else {
        rejected.push({
          ...row,
          status: 'error',
          error: translate('resources.upload.unsupported', {
            _: 'Unsupported file type',
          }),
        })
      }
    })
    if (added.length === 0 && rejected.length === 0) return
    setRows((current) => [...rejected, ...added, ...current])
    if (added.length === 0) return
    queueRef.current.push(...added)
    drainQueue()
  }

  const handleDrop = (event) => {
    event.preventDefault()
    setDragging(false)
    addFiles(event.dataTransfer.files)
  }

  const handleInputChange = (event) => {
    addFiles(event.target.files)
    event.target.value = ''
  }

  return (
    // Handlers sit on the card itself: on the inner zone a drop would bubble and add
    // the same files twice, and a drop on the padding would navigate away.
    <Paper
      className={classes.paper}
      onDragOver={(event) => {
        event.preventDefault()
        setDragging(true)
      }}
      onDragLeave={(event) => {
        // moving over a child fires dragleave on the container itself
        if (!event.currentTarget.contains(event.relatedTarget)) {
          setDragging(false)
        }
      }}
      onDrop={handleDrop}
    >
      <Title title="Upload" />
      <div className={clsx(classes.dropZone, dragging && classes.dragging)}>
        <CloudUploadIcon className={classes.icon} />
        <Typography>
          {translate('resources.upload.dropzone', {
            _: 'Drag audio files here',
          })}
        </Typography>
        <Button
          variant="contained"
          color="primary"
          onClick={() => inputRef.current.click()}
        >
          {translate('resources.upload.selectFiles', { _: 'Select files' })}
        </Button>
        <input
          ref={inputRef}
          type="file"
          multiple
          accept={ACCEPT}
          style={{ display: 'none' }}
          onChange={handleInputChange}
        />
      </div>
      {rows.length > 0 && (
        <div className={classes.rows}>
          {rows.map((row) => (
            <div key={row.id} className={classes.row}>
              <div className={classes.name}>
                <Typography variant="body2">{row.name}</Typography>
                {row.error && (
                  <Typography variant="caption" className={classes.error}>
                    {row.error}
                  </Typography>
                )}
              </div>
              <Typography variant="body2" className={classes.size}>
                {formatBytes(row.size)}
              </Typography>
              <Typography
                variant="body2"
                className={clsx(
                  classes.status,
                  row.status === 'error' && classes.error,
                )}
              >
                {translate(`resources.upload.status.${row.status}`, {
                  _: STATUS_LABELS[row.status],
                })}
              </Typography>
            </div>
          ))}
        </div>
      )}
    </Paper>
  )
}

export default UploadPage
