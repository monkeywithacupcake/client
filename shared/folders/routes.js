// @flow
import {makeRouteDefNode} from '../route-tree'
import {PrivateFolders, PublicFolders, TeamFolders} from './container'
import Files from './files'
import PaperKey from './files/paperkey'

const filesSubTree = (darkStatusBarContent = false) => ({
  files: {
    component: Files,
    children: {
      paperkey: {
        component: PaperKey,
      },
    },
  },
})

const routeTree = makeRouteDefNode({
  defaultSelected: 'private',
  children: {
    private: {
      component: PrivateFolders,
      initialState: {showingIgnored: false},
      children: filesSubTree(),
    },
    public: {
      component: PublicFolders,
      initialState: {showingIgnored: false},
      children: filesSubTree(true),
    },
    team: {
      component: TeamFolders,
      initialState: {showingIgnored: false},
      children: filesSubTree(),
    },
  },
})

export default routeTree
