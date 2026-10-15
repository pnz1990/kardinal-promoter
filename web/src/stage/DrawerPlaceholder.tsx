// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/DrawerPlaceholder.tsx — the stage drawer's place while its code or its
// pipeline loads: on a wide screen the drawer's column, which the page already
// gives up (useDrawerColumn), with the design system's Skeleton in it.
import { Skeleton } from '../ui'
import { useDrawerColumn, useWide } from './drawerColumn'

function Column({ env }: { env: string }) {
  useDrawerColumn()
  return <Skeleton label={`Loading the details of ${env}…`} className="app-drawer-loading" />
}

export function DrawerPlaceholder({ env }: { env: string }) {
  return useWide() ? <Column env={env} /> : null
}

function Reserve() {
  useDrawerColumn()
  return null
}

/** Takes the drawer column from the first render of a route that names an
 *  environment, before the pipeline, the map or the drawer's code arrives, so
 *  the page never narrows after it has painted (#1859, journey 024). */
export function DrawerColumnReserve({ active }: { active: boolean }) {
  const wide = useWide()
  return active && wide ? <Reserve /> : null
}
