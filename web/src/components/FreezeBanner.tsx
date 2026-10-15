// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// FreezeBanner.tsx — freezes (docs/agentic/freezes.md): a banner on every view while one
// holds promotions (who froze, whom it holds, since when, why), with Unfreeze, and the
// Freeze menu's two actions, Freeze agents and Stop the line. The controller enforces a
// freeze; the UI only shows and writes it (POST /freeze, /unfreeze: TokenReview mode with
// Helm ui.freezeControls, and the changewindows/freeze permission).
import { useCallback, useId, useState } from 'react'
import { api } from '../api/client'
import { useAccessIn } from '../access/access'
import { usePolling } from '../usePolling'
import type { AccessCheck, Freeze } from '../types'
import { Button, ConfirmDialog, MenuButton } from '../ui'
import { actionErrorMessage } from './ConfirmDialog'
import '../styles/FreezeBanner.css'

/** The check the freeze actions need; the controller checks it cluster-wide. */
export const FREEZE_CHECK: AccessCheck = { verb: 'update', resource: 'changewindows', subresource: 'freeze' }

/** How often the banner re-reads the freezes. */
const FREEZE_POLL_MS = 15_000

/** The sentence of one freeze: "Agents are frozen in every namespace by alice since … : INC-4211". */
export function freezeSentence(f: Freeze): string {
  const who = f.actors === 'agents' ? 'Agents are frozen' : 'The line is stopped for everyone'
  const where = f.namespaces.length === 0 ? 'in every namespace' : `in ${f.namespaces.join(', ')}`
  const until = f.until ? ` until ${f.until}` : ''
  const by = f.createdBy ? ` by ${f.createdBy}` : ''
  const exempt = f.exemptions > 0 ? ` (${f.exemptions} exemption${f.exemptions === 1 ? '' : 's'} in effect)` : ''
  return `${who} ${where}${by} since ${f.since}${until}${f.reason ? `: ${f.reason}` : ''}${exempt}`
}

/** The freezes, re-read every FREEZE_POLL_MS; refresh reads them now. */
export function useFreezes(): { freezes: Freeze[]; refresh: () => void } {
  const [freezes, setFreezes] = useState<Freeze[]>([])
  const load = useCallback(async () => {
    try {
      setFreezes((await api.listFreezes()).freezes ?? [])
    } catch {
      // A controller without the endpoint, or no read rights: no banner.
    }
  }, [])
  usePolling(load, FREEZE_POLL_MS)
  return { freezes, refresh: () => { void load() } }
}

/** The banner of the active freezes, with Unfreeze for those allowed. */
export function FreezeBanner({ freezes, onChange }: { freezes: Freeze[]; onChange: () => void }) {
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
    <div role="alert" data-testid="freeze-banner" className="freeze-banner">
      {active.map(f => (
        <div key={f.name} className="freeze-banner__row" data-actors={f.actors}>
          <span className="freeze-banner__text">{freezeSentence(f)}</span>
          {may.allowed && (
            <Button variant="secondary" size="sm" onClick={() => { setError(undefined); setLifting(f) }}>Unfreeze</Button>
          )}
          {!may.allowed && <code className="freeze-banner__cmd">kardinal unfreeze {f.name}</code>}
        </div>
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
