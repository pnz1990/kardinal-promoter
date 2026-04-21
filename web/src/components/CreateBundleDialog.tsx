// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// components/CreateBundleDialog.tsx — UI Bundle creation dialog (#917).
// Allows platform engineers to create a Bundle directly from the UI
// without CLI access — Kargo parity for competitive evaluations.
import { useState, useCallback, type FormEvent } from 'react'
import { api } from '../api/client'

interface CreateBundleDialogProps {
  /** Pipeline name to create the Bundle for. */
  pipelineName: string
  /** Pipeline namespace. */
  namespace: string
  /** Called after the Bundle is successfully created — triggers a re-poll. */
  onDone: () => void
  /** Called when the dialog is dismissed without creating a Bundle. */
  onCancel: () => void
}

/**
 * CreateBundleDialog renders a form for creating a Bundle CRD from the UI.
 * Fields: image (required), commitSHA (optional), author (optional).
 * On submit, calls POST /api/v1/ui/bundles and triggers onDone on success.
 * Inline error display on API failure. Does not close on error.
 */
export function CreateBundleDialog({ pipelineName, namespace, onDone, onCancel }: CreateBundleDialogProps) {
  const [image, setImage] = useState('')
  const [commitSHA, setCommitSHA] = useState('')
  const [author, setAuthor] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | undefined>()
  const [imageError, setImageError] = useState<string | undefined>()

  const handleSubmit = useCallback(async (e: FormEvent) => {
    e.preventDefault()
    setImageError(undefined)
    setError(undefined)

    if (!image.trim()) {
      setImageError('Image reference is required.')
      return
    }

    setLoading(true)
    try {
      await api.createBundle(
        pipelineName,
        image.trim(),
        commitSHA.trim() || undefined,
        author.trim() || undefined,
        namespace,
      )
      onDone()
    } catch (err) {
      setError(String(err))
    } finally {
      setLoading(false)
    }
  }, [image, commitSHA, author, pipelineName, namespace, onDone])

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="create-bundle-title"
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.7)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000,
      }}
    >
      <form
        onSubmit={handleSubmit}
        style={{
          background: 'var(--color-bg)',
          border: '1px solid #1e293b',
          borderRadius: '8px',
          padding: '1.5rem',
          maxWidth: '460px',
          width: '90%',
        }}
      >
        <h2
          id="create-bundle-title"
          style={{ margin: '0 0 0.25rem', fontSize: '1rem', fontWeight: 700, color: 'var(--color-text)' }}
        >
          Create Bundle — {pipelineName}
        </h2>
        <p style={{ color: 'var(--color-text-muted)', fontSize: '0.8rem', margin: '0 0 1rem' }}>
          Creates a new Bundle to start a promotion through all pipeline environments.
        </p>

        {/* Image input — required */}
        <label
          htmlFor="bundle-image"
          style={{ display: 'block', fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.25rem' }}
        >
          Container image <span style={{ color: '#ef4444' }}>*</span>
        </label>
        <input
          id="bundle-image"
          type="text"
          value={image}
          onChange={e => { setImage(e.target.value); setImageError(undefined) }}
          placeholder="ghcr.io/example/app:sha-abc1234"
          aria-describedby={imageError ? 'bundle-image-error' : undefined}
          style={{
            width: '100%', boxSizing: 'border-box',
            background: 'var(--color-surface)', border: `1px solid ${imageError ? '#ef4444' : '#334155'}`,
            borderRadius: '6px', padding: '0.5rem 0.75rem',
            color: 'var(--color-text)', fontSize: '0.875rem',
            marginBottom: imageError ? '0.25rem' : '0.75rem',
            fontFamily: 'monospace',
          }}
        />
        {imageError && (
          <div
            id="bundle-image-error"
            role="alert"
            style={{ color: '#ef4444', fontSize: '0.75rem', marginBottom: '0.75rem' }}
          >
            {imageError}
          </div>
        )}

        {/* Commit SHA — optional */}
        <label
          htmlFor="bundle-commit-sha"
          style={{ display: 'block', fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.25rem' }}
        >
          Commit SHA <span style={{ color: 'var(--color-text-muted)', fontStyle: 'italic' }}>(optional)</span>
        </label>
        <input
          id="bundle-commit-sha"
          type="text"
          value={commitSHA}
          onChange={e => setCommitSHA(e.target.value)}
          placeholder="abc1234"
          style={{
            width: '100%', boxSizing: 'border-box',
            background: 'var(--color-surface)', border: '1px solid #334155',
            borderRadius: '6px', padding: '0.5rem 0.75rem',
            color: 'var(--color-text)', fontSize: '0.875rem',
            marginBottom: '0.75rem',
            fontFamily: 'monospace',
          }}
        />

        {/* Author — optional */}
        <label
          htmlFor="bundle-author"
          style={{ display: 'block', fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.25rem' }}
        >
          Author <span style={{ color: 'var(--color-text-muted)', fontStyle: 'italic' }}>(optional)</span>
        </label>
        <input
          id="bundle-author"
          type="text"
          value={author}
          onChange={e => setAuthor(e.target.value)}
          placeholder="alice"
          style={{
            width: '100%', boxSizing: 'border-box',
            background: 'var(--color-surface)', border: '1px solid #334155',
            borderRadius: '6px', padding: '0.5rem 0.75rem',
            color: 'var(--color-text)', fontSize: '0.875rem',
            marginBottom: '1rem',
          }}
        />

        {/* API error */}
        {error && (
          <div
            role="alert"
            style={{
              color: '#ef4444', fontSize: '0.8rem', marginBottom: '0.75rem',
              background: '#1e0c0c', border: '1px solid #7f1d1d', borderRadius: '4px',
              padding: '0.5rem 0.75rem',
            }}
          >
            {error}
          </div>
        )}

        {/* Buttons */}
        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <button
            type="button"
            onClick={onCancel}
            disabled={loading}
            aria-label="Cancel bundle creation"
            style={{
              padding: '0.45rem 1rem',
              background: 'transparent',
              border: '1px solid #334155',
              borderRadius: '6px',
              color: 'var(--color-text-muted)',
              cursor: loading ? 'not-allowed' : 'pointer',
              fontSize: '0.875rem',
            }}
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={loading}
            aria-label="Create bundle"
            style={{
              padding: '0.45rem 1rem',
              background: '#0c1a2e',
              border: '1px solid #2563eb',
              borderRadius: '6px',
              color: '#93c5fd',
              cursor: loading ? 'not-allowed' : 'pointer',
              fontSize: '0.875rem',
              fontWeight: 600,
              opacity: loading ? 0.6 : 1,
            }}
          >
            {loading ? 'Creating…' : 'Create Bundle'}
          </button>
        </div>
      </form>
    </div>
  )
}

// ─── CreateBundleButton ───────────────────────────────────────────────────────

interface CreateBundleButtonProps {
  /** Pipeline name. */
  pipelineName: string
  /** Pipeline namespace. */
  namespace: string
  /** Called after a Bundle is successfully created — triggers a re-poll. */
  onRefresh: () => void
}

/**
 * CreateBundleButton renders a "+ Bundle" button in the pipeline header.
 * Clicking opens the CreateBundleDialog.
 */
export function CreateBundleButton({ pipelineName, namespace, onRefresh }: CreateBundleButtonProps) {
  const [showDialog, setShowDialog] = useState(false)

  return (
    <>
      <button
        onClick={() => setShowDialog(true)}
        aria-label={`Create bundle for ${pipelineName}`}
        style={{
          padding: '0.35rem 0.85rem',
          background: '#0c1a2e',
          border: '1px solid #1d4ed8',
          borderRadius: '6px',
          color: '#93c5fd',
          cursor: 'pointer',
          fontSize: '0.8rem',
          fontWeight: 600,
        }}
      >
        + Bundle
      </button>

      {showDialog && (
        <CreateBundleDialog
          pipelineName={pipelineName}
          namespace={namespace}
          onDone={() => {
            setShowDialog(false)
            onRefresh()
          }}
          onCancel={() => setShowDialog(false)}
        />
      )}
    </>
  )
}
