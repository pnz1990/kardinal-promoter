// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/drawerColumn.ts — whether the stage drawer is a column beside the page
// (the lg breakpoint and wider) or a sheet, and the class that has the page give
// up the column's width. In the entry chunk: the drawer's placeholder, shown
// while the drawer's code loads, takes the column the same way (#1859), so the
// page does not move when the drawer arrives.
import { useEffect, useLayoutEffect, useState } from 'react'
import { breakpoint } from '../tokens'

/** While a drawer column is open the page gives up its width (StageDrawer.css). */
const OPEN_CLASS = 'k-stage-drawer-open'

/** Whether the viewport is wide enough for the drawer column. */
export function useWide(): boolean {
  const query = `(min-width: ${breakpoint.lg}px)`
  const [wide, setWide] = useState(() => typeof window !== 'undefined' && !!window.matchMedia?.(query).matches)
  useEffect(() => {
    const mq = window.matchMedia?.(query)
    if (!mq) return
    const on = () => setWide(mq.matches)
    mq.addEventListener?.('change', on)
    return () => mq.removeEventListener?.('change', on)
  }, [query])
  return wide
}

/** Has the page give up the drawer column while the caller is mounted. A
 *  layout effect, so the placeholder handing over to the drawer never paints
 *  a frame without it. */
export function useDrawerColumn() {
  useLayoutEffect(() => {
    document.documentElement.classList.add(OPEN_CLASS)
    return () => document.documentElement.classList.remove(OPEN_CLASS)
  }, [])
}
