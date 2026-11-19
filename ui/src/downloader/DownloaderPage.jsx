import React from 'react'
import { makeStyles } from '@material-ui/core'
import config from '../config'
import { baseUrl } from '../utils/urls'

const useStyles = makeStyles({
  frame: {
    width: '100%',
    // The layout header and the player bar are fixed: fill what is left over.
    height: 'calc(100vh - 112px)',
    border: 0,
    display: 'block',
  },
})

// The downloader runs in the same container and is proxied under /downloader, so
// the frame is same-origin and the downloader keeps its own session cookie. A
// relative setting only marks it as same-origin: the proxy mount is the path.
const DownloaderPage = () => {
  const classes = useStyles()
  const src = /^https?:/i.test(config.downloaderUrl)
    ? config.downloaderUrl
    : baseUrl('/downloader/')
  return <iframe className={classes.frame} src={src} title="Downloader" />
}

export default DownloaderPage
