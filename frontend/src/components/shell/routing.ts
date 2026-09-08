// 路径推导小件（壳层共用）：App 顶条面包屑与侧栏高亮都从 pathname 推导「当前一级区段」
// 与「当前工作区 id」，此前双端各抄一份，收敛于此。纯函数、无副作用。

/** @description 一级路径归属：/workspaces/:id/changes → /workspaces——侧栏高亮与面包屑
 *              视图名同源，/workspaces/:id/... 下所有子页都归到「工作区」入口 */
export function activeRootOf(pathname: string): string {
    return '/' + (pathname.split('/')[1] ?? '')
}

/** @description 当前工作区 id：/workspaces/:id/... 首段捕获；非工作区页返回 undefined */
export function workspaceIdOf(pathname: string): string | undefined {
    return pathname.match(/^\/workspaces\/([^/]+)/)?.[1]
}
