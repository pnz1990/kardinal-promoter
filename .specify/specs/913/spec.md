# Spec: feat(ui): In-cluster kubectl port-forward UX (issue #913)

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future — UI authentication gaps`
- **Implements**: In-cluster `kubectl port-forward` UX (🔲 → ✅)

## Zone 1 — Obligations (falsifiable)

1. **O1 — HTTP warning banner**: When `window.location.protocol !== 'https:'` and the
   origin is not `localhost` or `127.0.0.1`, the UI renders a dismissible banner at the
   top of the page with a warning message indicating the connection is insecure.

2. **O2 — Localhost exemption**: When accessed at `http://localhost:8082` (or
   `http://127.0.0.1:8082`), the banner is NOT shown. Port-forward to localhost is the
   documented access method and is not considered insecure.

3. **O3 — HTTPS exemption**: When accessed at `https://...`, the banner is NOT shown.

4. **O4 — Dismissible**: The banner has a dismiss button. Once dismissed (within the session),
   it does not reappear until the page is reloaded.

5. **O5 — Accessible**: The banner has `role="alert"`, the message is readable by screen readers,
   and the dismiss button has an `aria-label`.

6. **O6 — Documentation**: `docs/installation.md` has an "Accessing the UI" section that
   documents `kubectl port-forward svc/kardinal-controller 8082` as the supported access method
   for in-cluster deployments without Ingress.

7. **O7 — Test coverage**: Unit tests verify O1 (banner shown on HTTP non-localhost),
   O2 (no banner on localhost), O3 (no banner on HTTPS).

## Zone 2 — Implementer's judgment

- Exact banner copy/wording
- Banner styling (follow BlockedBanner pattern)
- Whether to use a hook or inline component for the detection logic
- CSS variables vs inline styles (follow existing patterns)

## Zone 3 — Scoped out

- Persistent banner dismissal across page reloads (localStorage)
- Banner for mixed content (HTTPS page loading HTTP resources)
- CLI `kardinal dashboard` command changes (out of scope for this item)
