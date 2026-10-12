// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// approvals/ApprovalsPage.tsx — #/approvals (U7): every approval waiting on
// the person at the UI, oldest wait first, each with Approve and Decline
// through the same dialog the stage drawer opens; below, the approvals
// waiting on someone else and why. The wait leads each row: it is what an
// approver clears first. j and k move between rows, a approves the row in
// focus (App routes the keys here through the handle).
import { forwardRef, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { useAccessIn } from '../access/access'
import { actionCheck, restriction } from '../access/actionAccess'
import { ApprovalDialog } from '../stage/ApprovalDialog'
import { ActionButton } from '../stage/ActionButton'
import { VersionLabel } from '../components/VersionLabel'
import type { AccessCheck, Bundle, Pipeline, PolicyGate } from '../types'
import { useChangeHalo } from '../useChangeHalo'
import { Stack } from '../ui'
import { serializeHash, EMPTY_URL_STATE } from '../useUrlState'
import { approvalItems, type ApprovalItem } from './approvalsModel'
import './ApprovalsPage.css'

export interface ApprovalsPageProps {
  gates: PolicyGate[]
  pipelines: Pipeline[]
  loading?: boolean
  /** Called after a decision, so the page refetches at once. */
  onChanged?: () => void
  now?: number
}

/** What the keyboard shortcuts do on this page. */
export interface ApprovalsHandle {
  /** Moves focus to the next (1) or previous (-1) row. */
  move: (dir: 1 | -1) => void
  /** Opens Approve for the row in focus (or the only row): true, or what to say instead. */
  approve: () => true | string
}

/** How long the environment has waited: "40s", "12m", "3h 5m", "2d 4h". */
export function waited(iso: string | undefined, now: number): string {
  const t = iso ? Date.parse(iso) : NaN
  if (isNaN(t)) return '—'
  const s = Math.max(0, Math.floor((now - t) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`
}

type Open = { item: ApprovalItem; decision: 'approve' | 'reject' }

export const ApprovalsPage = forwardRef<ApprovalsHandle, ApprovalsPageProps>(function ApprovalsPage(
  { gates, pipelines, loading, onChanged, now = Date.now() }, ref,
) {
  const namespaces = useMemo(() => [...new Set(gates.filter(g => g.approval && !g.template).map(g => g.namespace))], [gates])
  // One check per namespace: creating an Approval is what approving and declining need.
  const decide = actionCheck('approve', {})!
  const { can, identity } = useAccessIn(namespaces.map(ns => [ns, decide] as [string, AccessCheck]))
  const { mine, others } = approvalItems(gates, pipelines, identity, ns => can(ns, decide))
  const [open, setOpen] = useState<Open>()
  const [result, setResult] = useState<string>()
  const listRef = useRef<HTMLOListElement>(null)

  const reasonFor = (item: ApprovalItem, decision: 'approve' | 'reject') =>
    restriction(decision, can(item.namespace, decide), identity, item.gate, item.gateName)

  const rows = () => [...(listRef.current?.querySelectorAll<HTMLElement>('.appr-item__where') ?? [])]
  useImperativeHandle(ref, () => ({
    move: dir => {
      const r = rows()
      if (r.length === 0) return
      const at = r.findIndex(el => el === document.activeElement || el.closest('.appr-item')?.contains(document.activeElement))
      const next = at < 0 ? (dir > 0 ? 0 : r.length - 1) : Math.min(r.length - 1, Math.max(0, at + dir))
      r[next].focus()
    },
    approve: () => {
      if (mine.length === 0) {
        return others.length > 0 ? `${others.length} waiting on someone else; none on you.` : 'Nothing here waits on your approval.'
      }
      const at = rows().findIndex(el => el.closest('.appr-item')?.contains(document.activeElement))
      if (at < 0 && mine.length > 1) return `${mine.length} wait on you: select one first (j / k).`
      const item = mine[at < 0 ? 0 : at]
      const why = reasonFor(item, 'approve')
      if (why) return why
      setResult(undefined)
      setOpen({ item, decision: 'approve' })
      return true
    },
  }))

  return (
    <section className="appr" aria-labelledby="appr-title">
      <Stack as="header" gap="2" className="appr__head">
        <p className="appr__eyebrow">Approvals</p>
        <h1 id="appr-title" className="appr__title">
          {identity === null
            ? (mine.length === 0 ? 'Nothing waits for approval' : `${mine.length} waiting for approval`)
            : (mine.length === 0 ? 'Nothing waits on you' : `${mine.length} waiting on you`)}
        </h1>
        {identity === null && (
          <p className="appr__muted">
            The UI does not know who you are, so it cannot record an approval: sign in with your Kubernetes token
            (TokenReview mode), or approve with <code>kardinal approve</code>.
          </p>
        )}
      </Stack>

      {loading && gates.length === 0 ? (
        <p className="appr__muted">Loading approvals…</p>
      ) : mine.length === 0 ? (
        <p className="appr__muted">An approval you can give appears here as soon as a gate asks for it, oldest first.</p>
      ) : (
        <ol ref={listRef} className="appr__list" aria-label="Waiting on you">
          {mine.map(item => (
            <Row key={item.gate.name} item={item} now={now}
              approveReason={reasonFor(item, 'approve')} declineReason={reasonFor(item, 'reject')}
              onDecide={decision => { setResult(undefined); setOpen({ item, decision }) }} />
          ))}
        </ol>
      )}

      {result && <p role="status" className="appr__muted">{result}</p>}

      {others.length > 0 && (
        <details className="appr__others">
          <summary>{others.length} waiting on someone else</summary>
          <ol className="appr__list appr__list--others" aria-label="Waiting on someone else">
            {others.map(item => <Row key={item.gate.name} item={item} now={now} />)}
          </ol>
        </details>
      )}

      {open && (
        <ApprovalDialog decision={open.decision} gate={open.item.gate} bundle={open.item.bundle} version={open.item.version}
          env={open.item.env} namespace={open.item.namespace}
          onCancel={() => setOpen(undefined)}
          onDone={msg => { setOpen(undefined); setResult(msg); onChanged?.() }} />
      )}
    </section>
  )
})

function Row({ item, now, approveReason, declineReason, onDecide }: {
  item: ApprovalItem; now: number; approveReason?: string; declineReason?: string
  onDecide?: (decision: 'approve' | 'reject') => void
}) {
  const changed = useChangeHalo(`${item.approved}/${item.required}`)
  const href = serializeHash({ ...EMPTY_URL_STATE, pipeline: item.pipeline, ns: item.namespace, env: item.env, section: 'why' })
  const approvers = (item.gate.approval?.decisions ?? []).filter(d => d.counted && d.decision === 'approve').map(d => d.user)
  return (
    <li className="appr-item" data-changed={changed || undefined}>
      <Stack gap="0" className="appr-item__wait" title={item.waitingSince ? `waiting since ${item.waitingSince}` : undefined}>
        <span className="appr-item__waited">{waited(item.waitingSince, now)}</span>
        <span className="appr__eyebrow">waiting</span>
      </Stack>
      <Stack gap="1" className="appr-item__body">
        <a className="appr-item__where" href={href} aria-label={`${item.pipeline}, ${item.env}`}>
          {item.pipeline} <span aria-hidden="true">›</span> {item.env}
        </a>
        <Stack direction="row" gap="2" wrap align="center" className="appr__muted">
          {/* The version as everywhere else (#1767): what it ships, the Bundle name after it. */}
          <VersionLabel bundle={{ name: item.bundle, version: item.version } as Bundle} />
          <span aria-hidden="true">·</span>
          <span>{item.gateName}</span>
        </Stack>
        <Stack direction="row" gap="2" wrap align="center" className="appr__muted">
          <Stack direction="row" gap="1" role="img" aria-label={`${item.approved} of ${item.required} approvals`}>
            {Array.from({ length: Math.min(item.required, 10) }, (_, i) => (
              <span key={i} className={i < item.approved ? 'appr-seat appr-seat--filled' : 'appr-seat'} />
            ))}
          </Stack>
          <span>{item.approved} of {item.required}{approvers.length > 0 ? ` · ${approvers.join(', ')} approved` : ''}</span>
        </Stack>
        {item.notMine && <p className="appr__muted">{item.notMine}</p>}
      </Stack>
      {onDecide && (
        <Stack direction="row" gap="2" wrap align="center" className="appr-item__actions">
          <ActionButton kind="reject" label="Decline" className="k-button k-button--destructive k-button--sm"
            restricted={declineReason} onClick={() => onDecide('reject')} />
          <ActionButton kind="approve" label="Approve" className="k-button k-button--primary k-button--sm"
            restricted={approveReason} onClick={() => onDecide('approve')} />
        </Stack>
      )}
    </li>
  )
}
