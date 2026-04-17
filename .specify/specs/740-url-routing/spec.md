# Spec: URL Routing — Pipeline and Node Selection in Hash (#740)

## Zone 1 — Obligations (falsifiable)

1. **Pipeline selection persists in URL hash.**
   Selecting a pipeline via `PipelineList` updates `window.location.hash` to include
   `pipeline=<name>`. A page reload with that hash active must restore the same pipeline
   selection without any user action.
   _Violation_: Selecting a pipeline does not change the URL, OR reloading with
   `#pipeline=nginx-demo` shows the default (unselected) state.

2. **Node selection persists in URL hash.**
   Clicking a DAG node updates `window.location.hash` to include `node=<id>` (alongside
   any existing `pipeline=` param). A page reload with that hash active must restore
   the same node selection once graph data loads.
   _Violation_: Clicking a node does not update the URL, OR reloading with
   `#pipeline=p&node=n` shows no selected node.

3. **Back/forward navigation restores state.**
   Navigating with the browser back/forward buttons changes the selected pipeline/node to
   match the hash at that history entry (handled via `popstate` event).
   _Violation_: Back button does not change the dashboard selection.

4. **No external router dependency.**
   The implementation must not add React Router or any new npm routing dependency to
   `package.json`.
   _Violation_: `package.json` lists a new routing library.

5. **All existing frontend tests pass.**
   The change must not break any previously passing test. CI `frontend build` check must
   remain green.
   _Violation_: CI frontend build fails or existing test count drops.

6. **Unit tests cover `useUrlState` hook.**
   At least 5 unit tests exercise: empty init, init from hash, set pipeline, set node,
   clear node, back navigation.
   _Violation_: Fewer than 5 tests for the hook, or any of the above scenarios untested.

## Zone 2 — Implementer's Judgment

- Choice of hash vs pathname routing: hash chosen (no server-side routing required).
- Whether to support bundle diff panel URL param: deferred (not in scope for this item;
  see MISS finding — follow-up issue to add `bundle=` param).
- Internal hook API shape (`[state, setState]` vs object).
- Whether to use `pushState` vs `replaceState`: `pushState` chosen for back/forward.

## Zone 3 — Scoped Out

- Bundle diff panel URL persistence (filed as follow-up issue).
- Support for `#pipeline=x&bundle=y` deep links.
- Server-side route handling (no SSR, hash is client-only).
- Mobile/touch-specific behavior changes.
