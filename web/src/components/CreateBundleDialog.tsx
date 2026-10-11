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
import { useState, useCallback, useRef, type FormEvent } from 'react'
import { api } from '../api/client'
import { useModalFocus } from '../useModalFocus'
import { actionErrorMessage } from './ConfirmDialog'
import '../ui'
import '../styles/CreateBundleDialog.css'

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
  const formRef = useRef<HTMLFormElement>(null)
  useModalFocus(formRef, () => { if (!loading) onCancel() })

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
      setError(actionErrorMessage('create the bundle', err))
    } finally {
      setLoading(false)
    }
  }, [image, commitSHA, author, pipelineName, namespace, onDone])

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="create-bundle-title"
      className="k-scrim"
    >
      <form ref={formRef} onSubmit={handleSubmit} className="k-dialog create-bundle">
        <h2 id="create-bundle-title" className="k-dialog__title">
          Create Bundle — {pipelineName}
        </h2>
        <p className="k-dialog__description">
          Creates a new Bundle to start a promotion through all pipeline environments.
        </p>

        {/* Image input — required */}
        <label htmlFor="bundle-image" className="create-bundle__label">
          Container image <span className="create-bundle__required">*</span>
        </label>
        <input
          id="bundle-image"
          type="text"
          value={image}
          onChange={e => { setImage(e.target.value); setImageError(undefined) }}
          placeholder="ghcr.io/example/app:sha-abc1234"
          aria-describedby={imageError ? 'bundle-image-error' : undefined}
          aria-invalid={imageError ? true : undefined}
          className="create-bundle__input create-bundle__input--mono"
        />
        {imageError && (
          <div id="bundle-image-error" role="alert" className="create-bundle__field-error">
            {imageError}
          </div>
        )}

        {/* Commit SHA — optional */}
        <label htmlFor="bundle-commit-sha" className="create-bundle__label">
          Commit SHA <span className="create-bundle__optional">(optional)</span>
        </label>
        <input
          id="bundle-commit-sha"
          type="text"
          value={commitSHA}
          onChange={e => setCommitSHA(e.target.value)}
          placeholder="abc1234"
          className="create-bundle__input create-bundle__input--mono"
        />

        {/* Author — optional */}
        <label htmlFor="bundle-author" className="create-bundle__label">
          Author <span className="create-bundle__optional">(optional)</span>
        </label>
        <input
          id="bundle-author"
          type="text"
          value={author}
          onChange={e => setAuthor(e.target.value)}
          placeholder="alice"
          className="create-bundle__input"
        />

        {/* API error */}
        {error && (
          <div role="alert" className="k-alert k-alert--failed create-bundle__error">
            {error}
          </div>
        )}

        {/* Buttons */}
        <div className="k-dialog__footer">
          <button
            type="button"
            onClick={onCancel}
            disabled={loading}
            aria-label="Cancel bundle creation"
            className="k-button k-button--quiet k-button--md"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={loading}
            aria-busy={loading || undefined}
            aria-label="Create bundle"
            className="k-button k-button--primary k-button--md"
          >
            {loading ? 'Creating…' : 'Create Bundle'}
          </button>
        </div>
      </form>
    </div>
  )
}

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
        type="button"
        onClick={() => setShowDialog(true)}
        aria-label={`Create bundle for ${pipelineName}`}
        className="k-button k-button--secondary k-button--sm"
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
