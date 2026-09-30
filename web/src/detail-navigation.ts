// adjacentResource 只在打开详情的当前页寻找相邻项，缺失或越界时不猜测其他页资料。
export function adjacentResource<Resource extends { id: string }>(items: readonly Resource[], currentId: string, step: -1 | 1): Resource | null {
  const currentIndex = items.findIndex((item) => item.id === currentId)
  return currentIndex < 0 ? null : items[currentIndex + step] ?? null
}
