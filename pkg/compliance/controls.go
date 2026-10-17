// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package compliance maps the controls of change-management and AI
// standards to the kardinal records that evidence them (docs/design/
// 22-compliance.md section 3). The one table generates the tables of
// docs/compliance.md and, with kardinal audit evidence-pack, the pack's
// controls.json, so the page and the pack cannot disagree.
//
// kardinal does not certify anything: each control says what the customer
// still has to do (Control.Customer).
package compliance

// Status says whether kardinal ships the evidence of a row.
type Status string

const (
	// Shipped is evidence in a released kardinal.
	Shipped Status = "shipped"
	// Planned is evidence of an accepted spec not yet released. The user
	// page leaves planned rows out (spec 22, decision C7).
	Planned Status = "planned"
)

// Framework is a standard or regulation and the controls of it in scope.
type Framework struct {
	// ID is a stable key, as in controls.json.
	ID string `json:"id"`
	// Name is the standard's name.
	Name string `json:"name"`
	// Caveat, when set, says how far the mapping applies.
	Caveat string `json:"caveat,omitempty"`
	// Controls are the controls in scope.
	Controls []Control `json:"controls"`
}

// Control is one control of a framework.
type Control struct {
	// ID is the control's reference in its standard (CC8.1, CM-3, Art. 12).
	ID string `json:"id"`
	// Title is the control's name.
	Title string `json:"title"`
	// Requirement paraphrases what the control asks.
	Requirement string `json:"requirement"`
	// Evidence lists, per element of the control, the kardinal record
	// that evidences it.
	Evidence []Evidence `json:"evidence"`
	// Customer is what kardinal cannot do for the customer.
	Customer string `json:"customer"`
}

// Evidence is the kardinal record of one element of a control.
type Evidence struct {
	// Element is the part of the control (a point of focus, a sub-control).
	Element string `json:"element"`
	// Record says, in Markdown, which records and fields evidence it.
	Record string `json:"record"`
	// Kinds are the kardinal.io kinds whose objects hold the evidence; an
	// evidence pack holds every object of these kinds for the period.
	Kinds []string `json:"kinds,omitempty"`
	// Actions are the AuditEvent actions (spec.action) that record it.
	Actions []string `json:"actions,omitempty"`
	// Status says whether the evidence is released.
	Status Status `json:"status"`
}

// Frameworks returns the control mapping. Callers may modify the result.
func Frameworks() []Framework {
	return []Framework{soc2(), iso27001(), nist80053(), dora(), aiAct(), iso42001()}
}

// requireApproval is the customer's part of every approval row: kardinal
// records approvals only where an environment asks for one.
const requireApproval = "Requiring an approval on production: `approval: pr-review`, or a PolicyGate with `spec.approval`, on each production environment."

// requireSeparation is the customer's part of every separation row:
// excludeAuthor is off by default and compares the version's creator, not
// the commit's author.
const requireSeparation = "Setting `approval.excludeAuthor: true` on those gates; and, to keep a change's commit author from approving it, " +
	"the SCM's branch protection (required reviewers or code owners, no self-approval), which kardinal does not check."

// promotion is the promotion lifecycle every change-tracking row names.
var promotion = []string{"PromotionStarted", "PromotionSucceeded", "PromotionFailed", "PromotionSuperseded", "PromotionRejected"}

// tracked is the record of what changed where: the promotion AuditEvents
// and the version's provenance.
func tracked(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "`AuditEvent` promotion actions per version and environment, with the actor that caused them; " +
			"the version's `spec.provenance` (author, commit, CI run) and its verified creator (`spec.actor`, annotation `kardinal.io/created-by`)",
		Kinds:   []string{"AuditEvent", "Bundle"},
		Actions: promotion,
		Status:  Shipped,
	}
}

// approved is the record of the approval before production.
func approved(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "approval gates (`spec.approval` on a PolicyGate), the `Approval` objects and `status.approvals[]` " +
			"(who approved, and whether it counted), and `pr-review` environments with the `kardinal/gates` commit " +
			"status; an `auto` environment records none",
		Kinds:   []string{"PolicyGate", "Approval", "Pipeline"},
		Actions: []string{"ApprovalRecorded", "ApprovalRevoked", "GateEvaluated", "PRMerged"},
		Status:  Shipped,
	}
}

// separated is the record that the author did not approve their own change.
func separated(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "`approval.excludeAuthor` (off by default, except on the approval gates kardinal adds for agents' versions in " +
			"`agents: approve` environments) does not count an approval by the user who created the version " +
			"(`kardinal.io/created-by`): for a version CI creates, that is the CI identity, not the commit's author, whom " +
			"kardinal does not compare. With an actor stamp, an agent and its sponsor count as one party. " +
			"`status.approvals[].reason` says why an approval did not count",
		Kinds:   []string{"PolicyGate", "Approval", "Bundle", "AgentIdentity"},
		Actions: []string{"ApprovalRecorded"},
		Status:  Shipped,
	}
}

// emergency is the record of emergency changes.
func emergency(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "gate overrides (`spec.overrides[]`: who, why, an expiry the policy caps) and freeze exemptions " +
			"(`ChangeWindow.spec.freeze.exemptions[]`: who and why)",
		Kinds:   []string{"PolicyGate", "ChangeWindow"},
		Actions: []string{"GateOverridden", "FreezeExempted"},
		Status:  Shipped,
	}
}

// tested is the record of the checks before a change is released.
func tested(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "the health check and bake of each environment (a PromotionStep reaches `Verified` only after them), " +
			"MetricChecks, upstream soak gates, and `Verdict`s from the verifiers a gate requires",
		Kinds:   []string{"PolicyGate", "Verdict", "Pipeline"},
		Actions: []string{"PromotionSucceeded", "PromotionFailed", "RollbackStarted", "VerdictRecorded", "VerdictRevoked"},
		Status:  Shipped,
	}
}

// receipts is the signed record of each promotion (spec 21).
func receipts(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "a signed `PromotionReceipt` per promotion per environment (v0.13, off by default: `receipts.enabled`), " +
			"written when the step reaches Verified and verifiable offline with `kardinal verify-receipt`; " +
			"`receipts.ndjson` in the evidence pack keeps each one with its signed envelope",
		Kinds:  []string{"PromotionReceipt"},
		Status: Shipped,
	}
}

// enforced is the record that the procedure is enforced, not advisory.
func enforced(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "a change reaches an environment only through a PromotionStep its Pipeline and gates allow; " +
			"the identity ValidatingAdmissionPolicies keep the names in the records from being forged (`admission.yaml` in the evidence pack)",
		Kinds:  []string{"Pipeline", "PolicyGate"},
		Status: Shipped,
	}
}

// access is the record of who may change what.
func access(element string) Evidence {
	return Evidence{
		Element: element,
		Record: "Kubernetes RBAC with the chart's roles (`<release>-viewer`, `-promoter`, `-approver`, `-freezer`, " +
			"`-verifier`, `-admin`) and virtual subresources (`pipelines/pause`, `pipelines/hold`, `policygates/override`, " +
			"`bundles/reject`, `changewindows/freeze`); the `<release>-scoped-writes` and `-graph-objects` policies; " +
			"each agent's scope (`AgentIdentity.spec.namespaces`, `environments`)",
		Kinds:  []string{"AgentIdentity"},
		Status: Shipped,
	}
}

func soc2() Framework {
	return Framework{
		ID:   "soc2",
		Name: "SOC 2 (2017 Trust Services Criteria)",
		Controls: []Control{{
			ID:    "CC8.1",
			Title: "Change management",
			Requirement: "Changes to infrastructure and software are authorised, designed, tested, approved and implemented " +
				"under control; points of focus include tracking changes, approving them, segregating the duties of " +
				"developers and approvers, and handling emergency changes.",
			Evidence: []Evidence{
				tracked("Changes are tracked"),
				approved("Approvals, where production requires them"),
				separated("The version's creator does not approve it (when `excludeAuthor` is set)"),
				emergency("Emergency changes are controlled"),
				tested("Changes are tested before release"),
				receipts("Evidence is tamper-evident"),
			},
			Customer: "The change policy itself, the review of the evidence, changes that bypass kardinal (direct pushes to " +
				"the GitOps branch: protect it and require the `kardinal/gates` status), and access reviews of who holds the " +
				"approver role. " + requireApproval + " " + requireSeparation,
		}},
	}
}

func iso27001() Framework {
	return Framework{
		ID:   "iso27001",
		Name: "ISO/IEC 27001:2022 Annex A",
		Controls: []Control{{
			ID:          "A.8.32",
			Title:       "Change management",
			Requirement: "Changes to information processing facilities and information systems are subject to change management procedures.",
			Evidence: []Evidence{
				enforced("The procedure is enforced, not advisory"),
				tracked("Changes are planned and recorded"),
				approved("Approvals, where production requires them"),
				{
					Element: "Rollback",
					Record:  "rollback versions (`spec.provenance.rollbackOf`) and their promotions; holds that keep an environment on a version (`Pipeline.spec.holds[]`)",
					Kinds:   []string{"Bundle", "Pipeline", "AuditEvent"},
					Actions: []string{"RollbackStarted", "RollbackSucceeded", "HoldCreated", "HoldReleased"},
					Status:  Shipped,
				},
				{
					Element: "Change windows",
					Record:  "`ChangeWindow` blackout and recurring windows, and freezes (who froze, who lifted, the exemptions)",
					Kinds:   []string{"ChangeWindow"},
					Actions: []string{"FreezeStarted", "FreezeLifted", "FreezeExempted"},
					Status:  Shipped,
				},
			},
			Customer: "The documented procedure, the risk assessment of a change, and communication to affected parties. " + requireApproval,
		}, {
			ID:          "A.5.3",
			Title:       "Segregation of duties",
			Requirement: "Conflicting duties and areas of responsibility are segregated.",
			Evidence: []Evidence{
				separated("The version's creator does not approve it (when `excludeAuthor` is set)"),
				{
					Element: "An agent and the person accountable for it count as one",
					Record:  "each agent's sponsor (`AgentIdentity.spec.sponsor`) and the approver's verified actor (`Approval.spec.actor`)",
					Kinds:   []string{"AgentIdentity", "Approval"},
					Status:  Shipped,
				},
				{
					Element: "Distinct roles",
					Record:  "the chart's user roles (`<release>-viewer`, `-promoter`, `-approver`, `-admin`); approvers named by `approval.allowedUsers` and `allowedGroups`",
					Kinds:   []string{"PolicyGate"},
					Status:  Shipped,
				},
				{
					Element: "Quorum",
					Record:  "`approval.required: N` distinct parties",
					Kinds:   []string{"PolicyGate", "Approval"},
					Status:  Shipped,
				},
			},
			Customer: "Who is granted which role, and the access review. " + requireSeparation,
		}},
	}
}

func nist80053() Framework {
	return Framework{
		ID:   "nist-800-53",
		Name: "NIST SP 800-53 Rev. 5",
		Controls: []Control{{
			ID:    "CM-3",
			Title: "Configuration change control",
			Requirement: "Determine the types of controlled changes; review and approve them with their security impact in " +
				"mind; document the decisions; implement approved changes; retain the records; monitor and review change activity.",
			Evidence: []Evidence{
				enforced("Controlled changes are defined and implemented as approved"),
				approved("Approvals, where production requires them"),
				{
					Element: "Records are retained and change activity is reviewed",
					Record: "AuditEvents, kept as `--audit-retention-max-age` and `--audit-retention-max-per-pipeline` allow; " +
						"`kardinal audit summary`, `kardinal audit export` and `kardinal audit evidence-pack`",
					Kinds:   []string{"AuditEvent"},
					Actions: promotion,
					Status:  Shipped,
				},
			},
			Customer: "The configuration management plan, the change control board's charter, and the system's other components. " + requireApproval,
		}, {
			ID:          "CM-3(2)",
			Title:       "Testing, validation and documentation of changes",
			Requirement: "Test, validate and document changes before implementing them.",
			Evidence:    []Evidence{tested("Changes are tested before release")},
			Customer:    "The test plans themselves, and the tests that run outside kardinal (CI).",
		}, {
			ID:          "CM-5",
			Title:       "Access restrictions for change",
			Requirement: "Define, document, approve and enforce access restrictions associated with changes.",
			Evidence:    []Evidence{access("Who may change what")},
			Customer:    "Who is bound to which role, and the access review.",
		}, {
			ID:          "CM-5(1)",
			Title:       "Automated access enforcement and audit records",
			Requirement: "Enforce access restrictions automatically and keep audit records of the enforcement actions.",
			Evidence: []Evidence{{
				Element: "Enforcement and its records",
				Record: "ValidatingAdmissionPolicies that deny, not warn; `APIWrite` AuditEvents for the UI's and the Bundle " +
					"API's writes, refusals included. The records are best-effort: one dropped under overload is counted in " +
					"`kardinal_audit_events_dropped_total`",
				Kinds:   []string{"AuditEvent"},
				Actions: []string{"APIWrite"},
				Status:  Shipped,
			}},
			Customer: "The Kubernetes API server's own audit log, for writes made with kubectl.",
		}, {
			ID:          "AC-5",
			Title:       "Separation of duties",
			Requirement: "Identify the duties that need separation and define access authorisations to support it.",
			Evidence:    []Evidence{separated("The version's creator does not approve it (when `excludeAuthor` is set)")},
			Customer:    "Which duties need separating in the organisation, and who holds each role. " + requireSeparation,
		}},
	}
}

func dora() Framework {
	return Framework{
		ID:   "dora",
		Name: "DORA, Regulation (EU) 2022/2554",
		Caveat: "Change management is Article 9(4)(e) of the Regulation, detailed in the RTS on the ICT risk management " +
			"framework (Commission Delegated Regulation (EU) 2024/1774). Article 17 is the ICT-related incident management " +
			"process, which kardinal contributes records to but does not run.",
		Controls: []Control{{
			ID:    "Art. 9(4)(e)",
			Title: "ICT change management",
			Requirement: "Documented policies, procedures and controls for ICT change management: changes are recorded, " +
				"tested, assessed and approved in a controlled way, and emergency changes are handled and documented.",
			Evidence: []Evidence{
				tracked("Changes are recorded"),
				tested("Changes are tested"),
				approved("Approvals, where production requires them"),
				emergency("Emergency changes are handled and documented"),
			},
			Customer: "The ICT risk management framework and the change policy it requires. " + requireApproval,
		}, {
			ID:          "Art. 17",
			Title:       "ICT-related incident management process",
			Requirement: "Incidents are detected, managed, logged, classified and followed up.",
			Evidence: []Evidence{{
				Element: "The change record an investigation needs",
				Record:  "what changed where, by whom and when (the promotion AuditEvents and the version's actor), and the rollback that restored it",
				Kinds:   []string{"AuditEvent", "Bundle"},
				Actions: []string{"PromotionSucceeded", "RollbackStarted", "RollbackSucceeded"},
				Status:  Shipped,
			}, {
				Element: "Stopping changes during an incident",
				Record:  "`kardinal freeze --everyone` (who froze, why, who lifted it)",
				Kinds:   []string{"ChangeWindow"},
				Actions: []string{"FreezeStarted", "FreezeLifted"},
				Status:  Shipped,
			}},
			Customer: "Incident detection, classification and reporting to the authority (Articles 18 and 19).",
		}},
	}
}

func aiAct() Framework {
	return Framework{
		ID:   "eu-ai-act",
		Name: "EU AI Act, Regulation (EU) 2024/1689",
		Caveat: "These articles bind the providers and deployers of the AI systems the Act names (Articles 12 and 14: " +
			"high-risk systems; Article 50: systems that interact with people or generate content). A coding agent that " +
			"changes software is usually none of these. kardinal does not make anyone compliant with the AI Act: it gives " +
			"an organisation that runs agents the records these articles ask of AI systems, applied to the agents' changes.",
		Controls: []Control{{
			ID:          "Art. 12",
			Title:       "Record-keeping",
			Requirement: "The system allows the automatic recording of events (logs) over its lifetime, to identify risks and support monitoring.",
			Evidence: []Evidence{{
				Element: "The agents' promotions, approvals, verdicts and freezes are logged",
				Record: "AuditEvents with `actor.kind: agent`, the agent's sponsor and what it asserted about itself " +
					"(model, session, client), for what an agent does through kardinal: its versions and their promotions, " +
					"approvals, verdicts, freezes and holds, and its UI and Bundle API writes (`APIWrite`); `kardinal audit export`",
				Kinds:   []string{"AuditEvent", "AgentIdentity"},
				Actions: []string{"APIWrite", "PromotionStarted", "ApprovalRecorded", "VerdictRecorded", "FreezeStarted", "HoldCreated"},
				Status:  Shipped,
			}},
			Customer: "Deciding whether the Act applies to your systems, and their own documentation. The Kubernetes API " +
				"server's audit log, for an agent's direct writes (kubectl, or `kardinal mcp` with the agent's kubeconfig) to " +
				"a Pipeline, a PolicyGate or a ChangeWindow, which kardinal does not record.",
		}, {
			ID:          "Art. 14",
			Title:       "Human oversight",
			Requirement: "People can oversee, understand, override and stop the system (Article 14(4)(e): a stop button or a similar procedure).",
			Evidence: []Evidence{{
				Element: "A person approves, overrides or stops",
				Record: "`agents: approve` environments (a person approves an agent's change), each agent's autonomy, " +
					"suspending an AgentIdentity, `kardinal freeze --agents`, and stop-the-line (`--everyone`)",
				Kinds:   []string{"Pipeline", "AgentIdentity", "ChangeWindow", "Approval"},
				Actions: []string{"ApprovalRecorded", "FreezeStarted", "FreezeLifted"},
				Status:  Shipped,
			}},
			Customer: "Who is trained and assigned to oversee the agents.",
		}, {
			ID:          "Art. 50",
			Title:       "Transparency",
			Requirement: "People are told when they interact with AI, and AI-generated content is marked in a machine-readable way.",
			Evidence: []Evidence{{
				Element: "Agent-made changes are marked",
				Record:  "the actor stamp marks each version and decision an agent made (`spec.actor.kind: agent`), shown in the PR evidence and the UI",
				Kinds:   []string{"Bundle", "Approval", "Verdict"},
				Status:  Shipped,
			}, {
				Element: "Agent-made commits are attributed",
				Record:  "keyless signed commits that name the agent",
				Status:  Planned,
			}},
			Customer: "Telling the people who read the agents' output.",
		}},
	}
}

func iso42001() Framework {
	return Framework{
		ID:   "iso42001",
		Name: "ISO/IEC 42001:2023 (AI management system), Annex A",
		Caveat: "Listed by the Annex A control's title; check the clause numbers against your copy of the standard. " +
			"As for the AI Act, these records apply to the agents' changes, not to the agents themselves.",
		Controls: []Control{{
			ID:          "AI roles and responsibilities",
			Title:       "AI roles and responsibilities",
			Requirement: "Roles and responsibilities for AI are defined and allocated.",
			Evidence: []Evidence{{
				Element: "An accountable person per agent",
				Record:  "`AgentIdentity.spec.sponsor`, and the agent's scope",
				Kinds:   []string{"AgentIdentity"},
				Status:  Shipped,
			}},
			Customer: "The AI management system itself: policy, objectives, and risk and impact assessments.",
		}, {
			ID:          "AI system recording of event logs",
			Title:       "AI system recording of event logs",
			Requirement: "The organisation determines at which phases of the AI system life cycle event logs are recorded.",
			Evidence: []Evidence{{
				Element: "The agents' promotions, approvals, verdicts and freezes are logged",
				Record:  "AuditEvents with the agent's verified actor and sponsor, for what an agent does through kardinal",
				Kinds:   []string{"AuditEvent"},
				Actions: []string{"APIWrite", "PromotionStarted"},
				Status:  Shipped,
			}},
			Customer: "Logging of the AI systems themselves, outside kardinal, and the Kubernetes API server's audit log for an agent's direct writes.",
		}, {
			ID:          "Use of AI systems",
			Title:       "Use of AI systems",
			Requirement: "AI systems are used responsibly, as intended, with human oversight in operation.",
			Evidence: []Evidence{{
				Element: "Oversight in operation",
				Record:  "autonomy per environment (`agents: auto | approve | deny`), opt-in agent approvals, freezes and suspension",
				Kinds:   []string{"Pipeline", "AgentIdentity", "ChangeWindow"},
				Actions: []string{"FreezeStarted", "FreezeLifted"},
				Status:  Shipped,
			}},
			Customer: "The intended use of each agent and the responsible-use policy.",
		}},
	}
}
