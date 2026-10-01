export const workspaceViews = ['library', 'search', 'inbox', 'organized', 'archive', 'external', 'topics'] as const
export type WorkspaceView = typeof workspaceViews[number]

// workspaceViewFromHash 只接受已知的私有视图片段，公开页面或未知地址回退到资料列表。
export function workspaceViewFromHash(hash: string): WorkspaceView {
  const view = hash.replace(/^#/, '')
  return workspaceViews.find((candidate) => candidate === view) ?? 'library'
}
