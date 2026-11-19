import React from 'react'
import { Route } from 'react-router-dom'
import Personal from './personal/Personal'
import DownloaderPage from './downloader/DownloaderPage'

const routes = [
  <Route exact path="/personal" render={() => <Personal />} key={'personal'} />,
  <Route
    exact
    path="/downloader"
    render={() => <DownloaderPage />}
    key={'downloader'}
  />,
]

export default routes
