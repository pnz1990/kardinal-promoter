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
