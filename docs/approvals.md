# Require approvals

An approval gate waits for people. Put `spec.approval` on a gate template; the gate is ready only when its expression is true and enough allowed people have approved the version for the environment, and none of them rejected it. It works for `approval: auto` environments as for `pr-review` ones: the environment's PromotionStep is not created until the gate is ready.

<figure class="k-demo" id="demo-approve-override">
  <picture>
    <source media="(prefers-reduced-motion: reduce)" srcset="../assets/demos/approve-override.png">
    <img src="../assets/demos/approve-override.webp" alt="The UI: approving prod-eu from its stage drawer, then overriding the soak gate of prod-us after its confirmation." width="1280" height="800" loading="lazy">
  </picture>
  <figcaption>Approve a release, then override a gate · <code>kardinal approve checkout prod-eu</code></figcaption>
</figure>


```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: two-approvers
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
spec:
  expression: "true"            # or any expression: both must pass
  message: "prod needs two release managers"
  approval:
    required: 2                 # distinct people (default 1)
    allowedGroups: [release-managers]
    allowedUsers: [oidc:carol@example.com]
    excludeAuthor: true         # the Bundle's verified creator does not count
```

Approve with the CLI:

```bash
kardinal approve my-app-v1-29-0 --env prod --comment "checked the migration"
kardinal approve my-app-v1-29-0 --env prod --decision reject --comment "wait for INC-42"
kardinal approve my-app-v1-29-0 --env prod --revoke
```

```
Recorded: oidc:alice@example.com approves my-app-v1-29-0 for prod (Approval my-app-v1-29-0-prod-3f2a9c1b0d)
```

How it works:

- `kardinal approve` creates an `Approval` object in the Pipeline namespace, labelled with the version and environment, holding the version's UID (`spec.bundleUID`) and owned by the version (deleted with it). It records your Kubernetes username and groups, read from the API server with a SelfSubjectReview. The chart's `<release>-approvals` ValidatingAdmissionPolicy refuses an Approval whose `spec.user` is not the requester, whose `spec.groups` are not among the requester's groups, or whose labels do not match its spec, or that is owned by anything but the version it approves (its owner references cannot change later), and lets only its approver delete it (the garbage collector and the namespace controller excepted; kardinal's controller never deletes Approvals) ([Verified identity](guides/security.md#verified-identity)). An Approval cannot be changed: approving again with another decision replaces yours, and `--revoke` deletes it. The CLI finds your Approval by its labels and `spec.user`, never by name: anyone can create an object under the name it would use, so when that name is taken the API server generates one.
- The promotion Graph reads the version's Approvals with a selector `ref` node and copies those for the environment, for this version's UID and from approvers the gate allows (`allowedUsers`, `allowedGroups`; anyone when both are empty) into each approval gate instance (`spec.approvals`, at most 101; a gate with more than 100 blocks and says so). The allow-lists apply before the cap, so Approvals from people who may not approve cannot crowd out the ones that count. An Approval of an earlier version that had the same name never counts. A new or deleted Approval re-renders the gate at once. Approving before the version reaches the gate is fine.
- The gate counts a decision when its user is in `allowedUsers` or one of its groups is in `allowedGroups`, and, with `excludeAuthor`, the user is not the version's verified creator. Each user counts once. A counted `reject` blocks the gate whatever the approvals. Every decision that appears or is revoked writes an `ApprovalRecorded` or `ApprovalRevoked` AuditEvent.
- With neither `allowedUsers` nor `allowedGroups`, every Approval counts: RBAC on `approvals` decides who can approve, and that includes ServiceAccounts with `create` on `approvals`. Set an allow-list for a gate that only people may pass.
- Group membership is checked when the Approval is created: an approval stays valid after its approver leaves the group. Revoke it by deleting the Approval (its approver) or with a new version. Revoking only matters while the gate waits: once the gate passed and the environment's promotion started, a revoked approval does not stop or undo that promotion (reject the version, or roll back, for that).
- `excludeAuthor` compares approvers with the version's `kardinal.io/created-by` annotation, its verified creator. `kardinal create bundle`, `kardinal promote` and `kardinal rollback` set it to your Kubernetes username, and the chart's `<release>-bundle-creator` policy admits it only in the requester's name and never lets it change. The UI sets the authenticated UI user (`kardinal-ui` without per-user authentication), a Subscription `subscription:<name>`, the Bundle API's static token `bundle-api`, an automatic rollback `kardinal-controller`; only this release's controller ServiceAccount (and the exact usernames in Helm `admission.controllerUsernames`) may name a creator other than itself. A version without the annotation (created with `kubectl` or a GitOps tool without it) blocks a gate with `excludeAuthor`: `excludeAuthor cannot be enforced: the Bundle has no verified creator`. So does a version created by a kardinal component (`subscription:*`, `bundle-api`, `kardinal-ui`, `kardinal-controller`): no person is known to exclude. For CI, create the version as the CI identity: `kardinal create bundle` (or `kubectl create`) with the CI ServiceAccount's kubeconfig records that ServiceAccount, which the policy verifies; with the Bundle API, send the ServiceAccount's own token rather than the shared static token once TokenReview authentication of the Bundle API is enabled, so the controller records the authenticated caller.
- While it waits, the gate reason is `waiting for approvals: 1 of 2 (alice@example.com)` or `rejected by bob@example.com (wait for INC-42)`; once met, the expression's reason ends with `approved by alice@example.com, bob@example.com (2 of 2)`, which `kardinal explain`, the UI and the PR evidence show. `status.approvals` lists every decision, whether it counts, why not, and when the gate first saw it.
- The expression can read the count: `approvals.count`, `approvals.required`, `approvals.users` and `approvals.rejected` ([CEL context](reference/cel-context.md#approvals)).
- `kardinal override` still force-passes an approval gate, with its own audit record.

Who may approve: bind the chart's `<release>-approvals` ClusterRole (create, delete and read `approvals`, read Bundles, Pipelines and PolicyGates) with a RoleBinding in the Pipeline namespace. The Graph ServiceAccount needs `list` and `watch` on `approvals`, which the chart's `graph-applier` role has.

Trust: anyone allowed to impersonate users or groups (`impersonate` RBAC) can approve as anyone. The identity checks rely on the API server's authentication, so treat `impersonate` as full trust for approvals, overrides and rejections.

Limits:

- An Approval is for one version: the next version needs new approvals.
- In the UI, an approval gate in the Policy Gates panel shows its quorum: one pip per approval it needs, filled for each counted approval, with `1 of 2 approvals`, `Approved (2 of 2)` or `Rejected by bob@example.com`. Below it are who may approve and every decision: who decided, whether it counts and why not, the comment, and when the gate saw it. While the quorum is not met, it shows the `kardinal approve <bundle> --env <env>` command to copy.
- Approving from the UI: the panel has **Approve**, **Reject** (which asks first) and **Revoke mine**, with an optional comment (`POST /api/v1/ui/approvals`). **Revoke mine** removes only your own decision: with `delete` on approvals you still cannot revoke someone else's from the UI (the request finds nothing of yours and changes nothing), nor with `kubectl delete` (the approvals policy refuses it). The copied CLI command names the namespace (`-n`). The decision is recorded for the user the UI authenticated. This needs TokenReview mode (`ui.auth.tokenReview=true`): with a static UI token or no UI auth there is no person to record, and the UI refuses and points to `kardinal approve`. Groups are the ones TokenReview returned, which `allowedGroups` is matched against. The UI checks with a SubjectAccessReview that the user may `create` (and, to replace or revoke, `delete`) Approvals in the namespace, then the controller writes the Approval with the user's name and groups and the annotation `kardinal.io/recorded-via: ui`. The `<release>-approvals` policy admits such a write from the controller only (and the usernames in `admission.controllerUsernames`), and lets the controller revoke only Approvals with that annotation. A decision made with `kardinal approve` is revoked with `kardinal approve --revoke`.
- Upgrading: `helm upgrade` does not update CRDs. Apply `chart/kardinal-promoter/crds/kardinal.io_approvals.yaml` and the updated `kardinal.io_policygates.yaml` before you add `spec.approval` to a gate; a Graph with an approval gate cannot be built without the Approval CRD.
