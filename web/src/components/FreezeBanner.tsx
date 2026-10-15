// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// FreezeBanner.tsx — freezes (docs/agentic/freezes.md): a banner on every view while one
// holds promotions (who froze, whom it holds, since when, why), with Unfreeze, and the
// Freeze menu's two actions, Freeze agents and Stop the line. The controller enforces a
// freeze; the UI only shows and writes it (POST /freeze, /unfreeze: TokenReview mode with
// Helm ui.freezeControls, and the changewindows/freeze permission). Each freeze is a Banner
// (ui/Banner.tsx): who froze is an ActorBadge, the times are ages with the instant as
// their title. Without the right to lift it (or with ui.freezeControls off) the banner
// names kardinal unfreeze, with the controller's reason as its title.
import { useCallback, useEffect, useId, useState } from 'react'
import { api } from '../api/client'
import { useAccessIn } from '../access/access'
import { usePolling } from '../usePolling'
import type { AccessCheck, Freeze } from '../types'
import { actorOfUser } from '../actor'
import { ago, humanDuration } from '../timeFormat'
import { ActorBadge, Banner, Button, ConfirmDialog, MenuButton } from '../ui'
import { actionErrorMessage } from './ConfirmDialog'
import '../styles/FreezeBanner.css'

/** The check the freeze actions need; the controller checks it cluster-wide. */
export const FREEZE_CHECK: AccessCheck = { verb: 'update', resource: 'changewindows', subresource: 'freeze' }

/** How often the banner re-reads the freezes. */
const FREEZE_POLL_MS = 15_000

/** Whom a freeze holds and where: "Agents are frozen in every namespace". */
export function freezeHeadline(f: Freeze): string {
  const who = f.actors === 'agents' ? 'Agents are frozen' : 'The line is stopped for everyone'
  return `${who} ${f.namespaces.length === 0 ? 'in every namespace' : `in ${f.namespaces.join(', ')}`}`
}

/** When a freeze ends, at now: "ends in 2h", "ends when lifted". */
export function freezeEnds(f: Freeze, now: number = Date.now()): string {
  if (!f.until) return 'until it is lifted'
  const left = (Date.parse(f.until) - now) / 1000
  return Number.isNaN(left) ? `until ${f.until}` : `ends in ${humanDuration(left)}`
}

function FreezeRow({ f, may, onLift, now }: { f: Freeze; may: { allowed: boolean; known: boolean; reason?: string }; onLift: () => void; now: number }) {
  const who = actorOfUser(f.createdBy)
  const since = ago(f.since, now)
  return (
    <Banner
      tone={f.actors === 'everyone' ? 'failed' : 'blocked'}
      icon="pause"
      data-actors={f.actors}
      aria-label={`Freeze ${f.name}`}
      actions={may.known && may.allowed
        ? <Button variant="secondary" size="sm" onClick={onLift}>Unfreeze</Button>
        : <code title={may.reason}>kardinal unfreeze {f.name}</code>}
    >
      <strong>{freezeHeadline(f)}</strong>
      {who && <> by <ActorBadge actor={who} compact /></>}
      {since && <>, <time dateTime={f.since} title={f.since}>since {since}</time></>}
      {', '}{f.until ? <time dateTime={f.until} title={f.until}>{freezeEnds(f, now)}</time> : freezeEnds(f, now)}
      {f.reason && <>: {f.reason}</>}
      {f.exemptions > 0 && <> ({f.exemptions} exemption{f.exemptions === 1 ? '' : 's'} in effect)</>}
    </Banner>
  )
}

/** How long the views wait for the first read of the freezes (see useFreezes). */
const FIRST_READ_MS = 1000

/**
 * The freezes, re-read every FREEZE_POLL_MS; refresh reads them now. settled is
 * true once the first read answered (or failed), or after FIRST_READ_MS: until
 * then App keeps the view under the banner hidden, so a banner that arrives
 * with the first read does not push the page down (journey 027).
 */
export function useFreezes(): { freezes: Freeze[]; refresh: () => void; settled: boolean } {
  const [freezes, setFreezes] = useState<Freeze[]>([])
  const [settled, setSettled] = useState(false)
  const load = useCallback(async () => {
    try {
      setFreezes((await api.listFreezes()).freezes ?? [])
    } catch {
      // A controller without the endpoint, or no read rights: no banner.
    } finally {
      setSettled(true)
    }
  }, [])
  usePolling(load, FREEZE_POLL_MS)
  useEffect(() => {
    const t = window.setTimeout(() => setSettled(true), FIRST_READ_MS)
    return () => window.clearTimeout(t)
  }, [])
  return { freezes, refresh: () => { void load() }, settled }
}

/** The banner of the active freezes, with Unfreeze for those allowed. */
export function FreezeBanner({ freezes, onChange, className, now = Date.now() }: { freezes: Freeze[]; onChange: () => void
  /** A class from the page that places it, for its margins. */
  className?: string; now?: number }) {
  const access = useAccessIn([['default', FREEZE_CHECK]])
  const may = access.can('default', FREEZE_CHECK)
  const [lifting, setLifting] = useState<Freeze>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const active = freezes.filter(f => f.active)
  if (active.length === 0) return null
  const lift = async () => {
    if (!lifting) return
    setBusy(true)
    setError(undefined)
    try {
      await api.unfreeze(lifting.name)
      setLifting(undefined)
      onChange()
    } catch (e) {
      setError(actionErrorMessage(`lift ${lifting.name}`, e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div data-testid="freeze-banner" className={className ? `freeze-banner ${className}` : 'freeze-banner'}>
      {active.map(f => (
        <FreezeRow key={f.name} f={f} may={may} now={now} onLift={() => { setError(undefined); setLifting(f) }} />
      ))}
      <ConfirmDialog
        open={lifting !== undefined}
        title={`Lift ${lifting?.name ?? 'the freeze'}?`}
        body="Held promotions go on within a minute."
        confirmLabel="Unfreeze"
        onConfirm={() => { void lift() }}
        onCancel={() => setLifting(undefined)}
        busy={busy}
        error={error}
      />
    </div>
  )
}

/** Freeze agents and Stop the line, for those allowed (nothing otherwise). */
export function FreezeMenu({ onChange }: { onChange: () => void }) {
  const access = useAccessIn([['default', FREEZE_CHECK]])
  const may = access.can('default', FREEZE_CHECK)
  const [actors, setActors] = useState<'agents' | 'everyone'>()
  const [reason, setReason] = useState('')
  const [minutes, setMinutes] = useState(0)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const reasonId = useId()
  const endsId = useId()
  const typedId = useId()
  if (!may.known || !may.allowed) return null
  const open = (a: 'agents' | 'everyone') => { setActors(a); setReason(''); setMinutes(0); setTyped(''); setError(undefined) }
  const everyone = actors === 'everyone'
  const run = async () => {
    if (!actors) return
    if (!reason.trim()) { setError('A reason is required: every held step shows it.'); return }
    if (everyone && typed !== 'stop') { setError('Type stop to stop the line for everyone.'); return }
    setBusy(true)
    setError(undefined)
    try {
      await api.freeze({ actors, reason: reason.trim(), minutes: minutes || undefined })
      setActors(undefined)
      onChange()
    } catch (e) {
      setError(actionErrorMessage(everyone ? 'stop the line' : 'freeze agents', e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <MenuButton label="Freeze" className="freeze-menu">
        <Button variant="ghost" size="sm" onClick={() => open('agents')}>Freeze agents…</Button>
        <Button variant="ghost" size="sm" onClick={() => open('everyone')}>Stop the line…</Button>
      </MenuButton>
      <ConfirmDialog
        open={actors !== undefined}
        title={everyone ? 'Stop the line for everyone?' : 'Freeze agents?'}
        body={everyone
          ? 'No version promotes, in any namespace, until the freeze is lifted. Steps waiting for a merge or a health check finish.'
          : 'Versions agents created stop promoting, in every namespace, until the freeze is lifted. People’s versions carry on.'}
        confirmLabel={everyone ? 'Stop the line' : 'Freeze agents'}
        tone="destructive"
        onConfirm={() => { void run() }}
        onCancel={() => setActors(undefined)}
        busy={busy}
        error={error}
      >
        <div className="freeze-form">
          <label htmlFor={reasonId} className="freeze-form__label">Reason (required)</label>
          <input id={reasonId} type="text" className="freeze-form__input" value={reason} maxLength={1024}
            disabled={busy} required aria-required="true" placeholder="e.g. INC-4211: agents paused while we investigate"
            onChange={e => setReason(e.target.value)} />
          <label htmlFor={endsId} className="freeze-form__label">Ends</label>
          <select id={endsId} className="freeze-form__input" value={minutes} disabled={busy}
            onChange={e => setMinutes(Number(e.target.value))}>
            <option value={0}>When lifted</option>
            <option value={60}>After 1 hour</option>
            <option value={240}>After 4 hours</option>
            <option value={1440}>After 24 hours</option>
          </select>
          {everyone && (
            <>
              <label htmlFor={typedId} className="freeze-form__label">Type stop to confirm</label>
              <input id={typedId} type="text" className="freeze-form__input" value={typed} disabled={busy}
                autoComplete="off" onChange={e => setTyped(e.target.value)} />
            </>
          )}
        </div>
      </ConfirmDialog>
    </>
  )
}
