# Spec: Error Boundaries on Async Components (#747)

## Zone 1 — Obligations (falsifiable)

1. **ErrorBoundary component exists** at `web/src/components/ErrorBoundary.tsx`.
   _Violation_: File does not exist.

2. **DAGView is wrapped** in an ErrorBoundary. When DAGView throws a render error,
   the fallback renders "Graph failed to load" text and a Retry button. The app does not
   white-screen.
   _Violation_: Uncaught render error in DAGView causes a white-screen.

3. **PipelineList is wrapped** in an ErrorBoundary. When PipelineList throws, the fallback
   renders "Failed to load pipelines" text and a Retry button.
   _Violation_: Uncaught render error in PipelineList causes a white-screen.

4. **NodeDetail/StageDetailPanel is wrapped** in an ErrorBoundary. When NodeDetail throws,
   the fallback renders "Details unavailable" text and a Retry button.
   _Violation_: Uncaught render error in NodeDetail causes a white-screen.

5. **BundleTimeline is wrapped** in an ErrorBoundary. When BundleTimeline throws, the fallback
   renders "Timeline unavailable" text and a Retry button.
   _Violation_: Uncaught render error in BundleTimeline causes a white-screen.

6. **Retry button works**: clicking Retry unmounts and remounts the wrapped child
   (via key-incrementing pattern or componentDidCatch + setState).
   _Violation_: Retry button renders but does not recover the component.

7. **Unit tests cover the ErrorBoundary**: at least 3 tests verify:
   - Normal render (no error) passes through children
   - Error in child shows fallback with correct message
   - Retry button resets the boundary
   _Violation_: Fewer than 3 tests, or any of the above scenarios untested.

8. **Apache 2.0 header** on all new files.
   _Violation_: Header missing.

9. **No TypeScript errors** (`tsc --noEmit` passes).
   _Violation_: TypeScript compilation error.

## Zone 2 — Implementer's Judgment

- Implementation style: React class component (required for `componentDidCatch`) vs
  third-party library. Class component chosen — no new dependencies.
- Error logging: `console.error` in `componentDidCatch` (no remote error tracking needed).
- Fallback UI style: simple card matching existing EmptyState pattern.
- Whether to show error details: no (never show raw stack traces per issue spec).
- Whether BundleTimeline and NodeDetail share the same ErrorBoundary instance or separate ones.

## Zone 3 — Scoped Out

- Remote error monitoring (Sentry, Datadog).
- Error boundary around the entire App root (would hide startup crashes).
- WCAG axe-core accessibility check (separate issue #748).
- `Suspense`-based lazy loading (not in scope for this item).
