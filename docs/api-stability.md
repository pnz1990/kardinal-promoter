# API stability and the path to 1.0

Every kardinal API is `kardinal.io/v1alpha1`, and kardinal runs on kro's alpha Graph API. This
page covers four things:

- which kinds have held still, and which have changed;
- how a field is deprecated and removed;
- which upgrades a test proves;
- what 1.0 needs.

Each statement names the code or the test behind it. Where kardinal does not do something yet,
the page says so.

## One API version, no conversion

- **One version per CRD.** Each of the 25 CRDs in `config/crd/bases` has a single version,
  `v1alpha1`, which is both served and stored.
- **No conversion.** No CRD sets `spec.conversion`, so the strategy is `None`, and kardinal
  ships no conversion webhook.
- **What an upgrade does to stored objects.** It never converts them. A release applies the
  same version with a new schema. When the new schema no longer has a field, the API server
  drops it from stored objects. The [upgrade guide](upgrade.md) lists each such field for the
  release that removes it.
- **What the version name promises.** Kubernetes' API conventions let an alpha version change
  incompatibly in any release. kardinal already holds itself to the stricter
  [deprecation policy](#deprecation-policy) below, and a test enforces its removal rule. The
  version name does not promise it, though: that promise is part of [1.0](#10-criteria).

## Stability levels

The levels describe what happened to each kind's schema in the last three minor releases:
v0.11.0, v0.12.0 and v0.13.0. They were counted from `config/crd/bases` at the v0.10.0, v0.11.0
and v0.12.0 tags and at v0.13's tree.

- A **field** is a path in a version's OpenAPI schema.
- A **tightened rule** is a change after which the API server can refuse a value of an existing
  field that the earlier schema accepted: a new length limit, a CEL rule or a narrower type.
- A rule on a field added in the same release is not a tightening.

| Level | Meaning |
|---|---|
| **Settled** | Released for at least three minors. In the last three, no field was removed and no rule on an existing field was tightened: every change was an addition. |
| **Evolving** | Released for at least three minors, but in the last three a field was removed (through the deprecation policy) or a rule was tightened. |
| **New** | First released in v0.12.0 or v0.13.0, so it is too young to judge. Its fields may still change, under the deprecation policy. |

A level is a record of what happened, not a promise. Every kind at every level falls under the
same [deprecation policy](#deprecation-policy).

| Kind | Written by | Since | Level | v0.11 to v0.13 |
|---|---|---|---|---|
| [AgentIdentity](reference/api.md#agentidentity) | cluster admins | v0.12.0 | New | v0.13 adds 5 fields |
| [AgentIndex](reference/api.md#agentindex) | the chart, one per release; the controller writes its status | v0.12.0 | New | v0.13 adds 4 status fields |
| [AgentRun](reference/api.md#agentrun) | the Bundle's Graph, for a verify gate with `verify.run` | v0.12.0 | New | no change |
| [Approval](reference/api.md#approval) | people (`kardinal approve`, the UI) | v0.10.0 | Settled | fields added (10 in v0.12, 2 in v0.13) |
| [AuditEvent](reference/api.md#auditevent) | the controller; its spec is immutable | v0.8.0 | Settled | fields and actions added; v0.12 requires only one of `bundleName`, `pipelineName` and `environment`, where all three were required |
| [Bundle](reference/api.md#bundle) | CI (`kardinal create bundle`, the Bundle API), a Subscription, or the controller (rollbacks) | v0.1.0 | Evolving | v0.11 refuses a `chart` Bundle with a `configRef`; v0.12 limits `provenance.author` to 256 characters and `ciRunURL` to 2048; v0.13 refuses credentials in `ciRunURL`; fields added in each minor |
| [ChangeRequest](reference/api.md#changerequest) | the Bundle's Graph | v0.13.0 | New | new in v0.13 |
| [ChangeWindow](reference/api.md#changewindow) | platform teams | v0.5.0 | Evolving | v0.12 limits `reason` to 1024 characters; fields added in v0.12, `freeze` among them |
| [ClusterScmProvider](reference/api.md#clusterscmprovider) | cluster admins | v0.10.0 | Settled | no change |
| [HookRun](reference/api.md#hookrun) | the Bundle's Graph | v0.10.0 | Settled | no change |
| [ImageVerification](reference/api.md#imageverification) | the Bundle's Graph, from the Pipeline's `spec.imageVerification` | v0.10.0 | Settled | v0.13 adds 74 fields (attestations, VEX); its new rules check only those fields |
| [MetricCheck](reference/api.md#metriccheck) | users | v0.2.0 | Settled | no change |
| [NotificationHook](reference/api.md#notificationhook) | users | v0.9.0 | Evolving | v0.11 removes `spec.webhook.authorizationHeader`, deprecated in v0.10.0 |
| [PRStatus](reference/api.md#prstatus) | the controller | v0.5.0 | Settled | v0.12 adds 3 status fields |
| [Pipeline](reference/api.md#pipeline) | users | v0.1.0 | Evolving | v0.11 removes 23 fields, each deprecated in v0.9.0 or v0.10.0; v0.12 refuses an environment with a `fleet` and `update.strategy: argocd`, and lets `fleet.maxUnavailable` be a percentage; fields added in each minor (25, 79, 55) |
| [PolicyGate](reference/api.md#policygate) | users (templates); the Bundle's Graph (instances) | v0.1.0 | Evolving | v0.11 removes `spec.selector`, deprecated in v0.9.0; v0.12 limits `overrides[].reason` to 1024 characters and `overrides[].stage` to 63; fields added in each minor |
| [PromotionReceipt](reference/api.md#promotionreceipt) | the controller | v0.13.0 | New | new in v0.13 |
| [PromotionStep](reference/api.md#promotionstep) | the Bundle's Graph | v0.1.0 | Evolving | v0.11 removes `spec.region`, deprecated in v0.9.0; status fields added (3, 14, 159) |
| [RenderRun](reference/api.md#renderrun) | the Bundle's Graph | v0.10.0 | Settled | v0.13 path patterns also accept quoted keys, which loosens them |
| [RollbackPolicy](reference/api.md#rollbackpolicy) | users | v0.5.0 | Settled | no change |
| [ScheduleClock](reference/api.md#scheduleclock) | the chart (`kardinal-clock`) | v0.6.0 | Settled | no change |
| [ScmProvider](reference/api.md#scmprovider) | users | v0.10.0 | Settled | no change |
| [StepTemplate](reference/api.md#steptemplate) | cluster admins | v0.12.0 | New | no change |
| [Subscription](reference/api.md#subscription) | users | v0.6.0 | Settled | v0.12 adds 1 status field |
| [Verdict](reference/api.md#verdict) | verifiers: an AgentRun's agent, a person (`kardinal verify`), CI | v0.12.0 | New | no change |

**Kinds that kardinal writes.** You read these, and the Bundle's Graph or the controller writes
them: AgentRun, AuditEvent, ChangeRequest, HookRun, ImageVerification, PRStatus,
PromotionReceipt, PromotionStep and RenderRun.

- **Their spec.** It is the contract between the Graph kardinal renders and its reconcilers.
- **A release that changes how Graphs are rendered.** It bumps `graph.RenderVersion`
  (`pkg/graph/gates.go`). The new controller then re-renders the Graph of each promoting version
  once, so those versions' objects take the new shape.
- **The policy.** Their fields follow the same deprecation policy as the rest, because scripts
  and dashboards read them.

## Deprecation policy

### What kardinal has done

- **Removals with no deprecation.** v0.7.0, v0.8.0 and v0.9.0 removed fields without a release
  that deprecated them first:
  - v0.7.0: PromotionStep `spec.upstreamVerified`.
  - v0.8.0: PolicyGate `spec.upstreamEnvironment`.
  - v0.9.0, 14 fields, including:
    - AuditEvent `spec.actor` and `spec.bundleImage`;
    - Bundle `status.environments[].gateResults`, `mergedBy` and `prMergedAt`;
    - PromotionStep `spec.inputs`.
- **The one removal so far that followed a deprecation.** v0.11.0 removed 34 fields, and
  v0.10.0 had marked each of them deprecated. Most had been deprecated since v0.9.0. Two were
  deprecated only in v0.10.0: NotificationHook `spec.webhook.authorizationHeader`, and
  `health.cluster` on a fleet's targets. The changelog's v0.10.0
  **Deprecated** entry named v0.11 as the release that removes them, and its v0.11.0
  **Removed** entry lists them.
- **A deprecation that was reversed.** PolicyGate `spec.when` was marked deprecated in v0.9.0
  and v0.10.0. From v0.11.0 it is kept, ignored and unmarked, so that a Graph an earlier
  release built, whose gate instances still carry it, still compiles (`api/v1alpha1/policygate_types.go`).

### The policy

1. **How a field is deprecated.** A release marks it, and the changelog says so:
   - **A `Deprecated:` paragraph** in the field's Go doc comment, saying what to use instead and
     which release removes it. controller-gen copies it into the CRD's description, so
     `kubectl explain` and the [CRD API reference](reference/api.md) show it.
   - **A `docs/changelog.d` fragment** in section `Deprecated` that names the field and the
     release that removes it. v0.10.0's "Removed in v0.11" entry is the model.
   - **A warning wherever kardinal still reads the field.** In v0.10.0:
     - `kardinal validate` warned about `spec.git.provider` and `spec.when`;
     - the NotificationHook reconciler set `PlaintextCredential=True` for `authorizationHeader`;
     - the CLI flag `--emergency` of `kardinal rollback` used cobra's `MarkDeprecated`;
     - the deprecated chart values said so in `values.yaml` and `values.schema.json`.
2. **How long it keeps working.** A deprecated field keeps its schema through every patch
   release of the minor that deprecates it, and keeps behaving as it did: honoured, ignored or
   refused. The next minor is the earliest that may remove it, so you get at least one minor of
   notice.
3. **How it is removed.** Only in a minor release, with a `Removed` changelog fragment and an
   upgrade-guide step for objects that still set it. A patch release never removes a field.
4. **Tightened validation** on an existing field is not a removal:
   - **When and where it is announced.** It lands only in a minor release, and the upgrade guide
     lists it (for example, the free-text limits in
     [Upgrading from v0.11.0](upgrade.md#upgrading-from-v0110)).
   - **Stored objects stay writable while the field is unchanged.** This is CRD validation
     ratcheting, GA since Kubernetes 1.33; kardinal needs 1.35. Three tests check it:
     - `TestFreeTextLimitsRatchetOnAPIServer` (`test/helm`, on the oldest and newest supported
       API servers);
     - `TestCRDNewRulesRatchet` (`api/v1alpha1`);
     - the ratcheting table in `TestUpgrade_FromV081`.
   - **The exception.** A rule on an object's name blocks every write to an object stored
     against it. The [Kubernetes version](upgrade.md#kubernetes-version) table of the v0.8.1
     upgrade documents this for a PolicyGate name over 63 characters.

### What enforces it

`TestReleasedCRDFieldsKept` (`test/hack/api_fields_test.go`) enforces the policy:

- **What it compares.** It reads every field of the newest release's CRDs (`config/crd/bases`
  at the newest `vX.Y.Z` tag) and checks each against the tree's.
- **On main.** It fails on a field that is gone, unless that release marked it, or a field
  around it, deprecated. A CRD version marked `deprecated: true` counts too.
- **On a `release/vX.Y` branch whose minor is released.** It fails on any field that is gone,
  marked or not.
- **Checked against history.** `TestRemovedFieldsRuleOnHistory` runs the same rule from v0.10.0
  to v0.11.0. It passes, and with v0.10.0's marks cleared it reports all 34 removed fields.
- **The tags it needs.** CI fetches the release tags so the guard can run (`ci.yml`, "Fetch the
  release tags"). In CI it fails without them.

### What is not done today

- **No API-server warning.** Kubernetes has a deprecation warning per CRD version
  (`deprecated`, `deprecationWarning`), not per field, and kardinal has one version. A
  ValidatingAdmissionPolicy with the `Warn` action could warn when a deprecated field is set;
  kardinal ships none for this.
- **No `kardinal doctor` check.** Doctor does not list stored objects that set a deprecated
  field.
- **The CLI and the chart values.** This policy and its guard cover the CRDs. The CLI and the
  chart values have used the same signals (cobra's `MarkDeprecated`, a "Deprecated" value
  description), but no test holds them to a notice period.
- **What the guard checks.** It checks that fields still exist, not what they do. A tightened
  rule, a narrowed enum, a changed default, or a field that is kept but now ignored all pass it,
  as does a missing changelog fragment. Review and the upgrade guide cover those.

## Upgrade guarantees

### Tested paths

Each upgrade suite works the same way:

- it installs a release as published: its chart, its CLI and its kro;
- it builds an estate with versions in flight;
- it follows that release's section of the [upgrade guide](upgrade.md) with the guide's own
  commands;
- it upgrades straight to the build under test.

The release gate (`hack/release-gate`), run for each release before it is tagged, runs every
suite in `hack/e2e/matrix.txt`. It runs the upgrade suites on 1.35 and 1.37, the oldest and the
newest Kubernetes minors kardinal supports.

| From | Suite | Test |
|---|---|---|
| v0.8.1 | `upgrade` | `TestUpgrade_FromV081` |
| v0.9.0 | `upgrade-v090` | `TestUpgradeV090_FromV090` |
| v0.10.0 | `upgrade-v0100` | `TestUpgradeV0100_FromV0100` |
| v0.11.0 | `upgrade-v0110` | `TestUpgradeV0110_Estate` |
| v0.12.0 | `upgrade-v0120` | `TestUpgradeV0120_Estate` |
| v0.12.1 | `upgrade-v0121` | `TestUpgradeV0121_Estate` |
| v0.12.3 | `upgrade-v0123` | `TestUpgradeV0123_Estate` |

- **Skip-version upgrades are tested.** Each suite goes from its release straight to the
  current one; from v0.8.1 that skips four minors.
- **Not tested:**
  - an upgrade from v0.12.2 (v0.12.1 and v0.12.3 are tested, and none of the three changes a
    CRD, value or admission policy);
  - an upgrade from any release before v0.8.1;
  - an upgrade through each release in turn.
- **Versions in flight survive the upgrade.** The suites check that each version in flight keeps
  its objects and finishes. For example, `TestUpgradeV0120_Estate` checks all of these:
  - the same Bundle (uid), Graph, PromotionSteps and PR numbers;
  - gates that held a version keep holding it;
  - every version in flight reaches Verified;
  - every stored object reads with the new release's types.
- **Some upgrades need steps by hand, and the suites run them:**
  - from v0.8.1, step 1 finds the stored objects the new CRDs reject;
  - from v0.9.0 and v0.10.0, the git Secrets need the `kardinal.io/referenceable` label.

  The guide lists each release's steps, and says so when an upgrade needs none (from v0.12.0,
  a version without its verify gate "goes on after the upgrade with no step by hand").

### CRD conversion

There is none. No upgrade so far has changed the API version, so none needed conversion or a
storage migration. A second version (v1beta1 or v1) will need two things: a conversion strategy,
and an upgrade suite that starts with stored `v1alpha1` objects. Both are part of
[1.0](#10-criteria).

### Rolling back the controller

**Render labels make a rollback re-render.**

- Every Graph carries the render label of the controller that rendered it
  (`kardinal.io/render-version`, `graph.RenderLabel`).
- A controller from v0.12.3 on (#2271) re-renders, once, the Graph of each promoting version
  whose label differs from its own. Earlier controllers do not. The check is for equality, not "older than", so a downgraded controller also
  re-renders a newer controller's Graphs with its own rendering (`pkg/graph/gates.go`).
- Unit tests check this, including for a label written by another release:
  `TestRenderVersion_OnlyPromotingRetranslates`, `TestRenderVersion_StuckVersionRetranslatesOnce`
  and `TestRenderVersion_V0123LabelRetranslatesOnce`.

**A release branch never reuses a label of main's.**

- A patch release that changes the rendering sets a label main never uses, `N-vX.Y.Z`.
  `TestRenderLabelOnReleaseBranch` enforces this (#2346, #2359).
- Before that rule, v0.12.3 and main both wrote the label `2` with different renderings, so the
  upgrade from v0.12.3 re-rendered nothing.

**What a downgrade needs** is listed per release in [Downgrading](upgrade.md#downgrading-to-v090):

- Graphs with the compact shape;
- the reader labels;
- the ClusterRole that v0.13 removes, which is why the chart is rolled back with the
  controller (`helm rollback`).

**No test downgrades kardinal.**

## 1.0 criteria

kardinal reaches 1.0 when all of the following hold. **Today** says where each one stands.

1. **A stable API version.**
   - **The criterion:**
     - the kinds users write are served at `kardinal.io/v1`, and `v1alpha1` is still served;
     - a conversion strategy is in place;
     - an upgrade suite starts with stored `v1alpha1` objects and reads them at the new version.
   - **Today:** `v1alpha1` only, and no conversion.
2. **Settled core kinds.**
   - **The criterion:** Pipeline, Bundle and PolicyGate each go two minors with no field removed
     and no rule tightened. A kind that is still New at 1.0 stays out of the 1.0 promise and keeps
     `v1alpha1`.
   - **Today:** all three are Evolving.
3. **kro's Graph leaves alpha.**
   - **The criterion:** a kro release whose Graph API is beta or later, pinned at a final
     release.
   - **Today:** kardinal targets kro's Graph kind behind the alpha `GraphKind` feature gate, at
     the release candidate v0.10.0-rc.0 (`KRO_VERSION` in `hack/install-kro.sh`).
4. **Deprecation tooling.**
   - **The criterion:** the API server warns when a deprecated field is set, and
     `kardinal doctor` lists the stored objects that set one.
   - **Today:** neither exists ([What is not done today](#what-is-not-done-today)).
5. **The upgrade matrix.**
   - **The criterion:**
     - an upgrade suite from the latest patch of every minor in the support window;
     - a tested downgrade to the previous minor;
     - both on the oldest and the newest supported Kubernetes minors.
   - **Today:** suites from v0.8.1 through v0.12.3, with v0.12.2 the one release not tested;
     no downgrade test.
6. **A written support window.**
   - **The criterion:** how many minors get patch releases, and which Kubernetes minors are
     supported.
   - **Today:**
     - Kubernetes 1.35 or later (the chart's `kubeVersion`), tested on 1.35 and 1.37;
     - release/v0.12 got three patch releases, but no window is stated.
7. **Reviews.**
   - **The criterion:**
     - an external security review of the controller, its admission policies and the Bundle API;
     - a review of the reference docs against this page.
   - **Today:** neither has been done.
