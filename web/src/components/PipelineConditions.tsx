// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// PipelineConditions.tsx — the Pipeline's own problems, under its header, as `kardinal
// status <pipeline>` prints them: an error when it is not Ready (GitCredentialMissing says
// which git Secret to create), and a warning when its git Secret is not labelled
// kardinal.io/referenceable=true (still used in v0.10, refused in v0.11). The controller
// puts the command that fixes each in the condition's message; it is shown to copy.
import type { Condition } from '../types'
import { CodeBlock } from '../ui/Text'
import '../styles/PipelineConditions.css'

/** The kubectl command in a condition message: inside "(…)" when it starts there, else to the end. */
export function fixCommand(message: string | undefined): string | undefined {
  if (!message) return undefined
  const at = message.indexOf('kubectl ')
  if (at < 0) return undefined
  if (message[at - 1] === '(') {
    const end = message.indexOf(')', at)
    return message.slice(at, end < 0 ? undefined : end).trim()
  }
  return message.slice(at).trim()
}

interface Notice {
  key: string
  tone: 'failed' | 'warning'
  title: string
  message: string
  command?: string
}

/** The notices of a Pipeline's conditions: not Ready first, then the referenceable warning. */
export function pipelineNotices(conditions: Condition[] = []): Notice[] {
  const out: Notice[] = []
  const ready = conditions.find(c => c.type === 'Ready')
  if (ready && ready.status === 'False') {
    out.push({
      key: 'ready',
      tone: 'failed',
      title: ready.reason === 'GitCredentialMissing'
        ? 'Git has no token for this pipeline, so nothing can be promoted'
        : `This pipeline is not ready${ready.reason ? ` (${ready.reason})` : ''}`,
      message: ready.message ?? '',
      command: fixCommand(ready.message),
    })
  }
  const ref = conditions.find(c => c.type === 'SecretReferenceable')
  if (ref && ref.status === 'False') {
    out.push({
      key: 'referenceable',
      tone: 'warning',
      title: 'Label the git Secret before upgrading to v0.11',
      message: ref.message ?? '',
      command: fixCommand(ref.message),
    })
  }
  return out
}

export function PipelineConditions({ conditions }: { conditions?: Condition[] }) {
  const notices = pipelineNotices(conditions)
  if (notices.length === 0) return null
  return (
    <div className="pipeline-conditions">
      {notices.map(n => (
        <div key={n.key} role={n.tone === 'failed' ? 'alert' : 'status'} className="pipeline-conditions__notice" data-tone={n.tone}>
          <p className="pipeline-conditions__title">
            <span className="pipeline-conditions__badge">{n.tone === 'failed' ? 'Error' : 'Warning'}</span>
            {n.title}
          </p>
          {n.message && <p className="pipeline-conditions__message">{n.message}</p>}
          {n.command && <CodeBlock language="shell" copy>{n.command}</CodeBlock>}
        </div>
      ))}
    </div>
  )
}
