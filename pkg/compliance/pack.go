// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package compliance

// PackFiles maps each kind the control table names to the file of the
// evidence pack (kardinal audit evidence-pack) that holds its objects
// (docs/design/22-compliance.md section 4.1). PolicyGate maps to the gate
// instances; the gate templates in force are in policygate-templates.ndjson.
var PackFiles = map[string]string{
	"AuditEvent":    "auditevents.ndjson",
	"Approval":      "approvals.ndjson",
	"PolicyGate":    "policygates.ndjson",
	"Bundle":        "bundles.ndjson",
	"Pipeline":      "pipelines.ndjson",
	"ChangeWindow":  "changewindows.ndjson",
	"AgentIdentity": "agentidentities.ndjson",
	"Verdict":       "verdicts.ndjson",
	// AgentRun: the runs the pack's Verdicts name (spec.run). A Verdict an
	// in-cluster verifier wrote has no actor stamp; its AgentRun's status
	// names the agent and the sponsor.
	"AgentRun": "agentruns.ndjson",
	// PromotionReceipt: each receipt of the period's versions with its
	// signed envelope (status.bundle), which kardinal verify-receipt --file
	// checks offline.
	"PromotionReceipt": "receipts.ndjson",
	"ChangeRequest":    "changerequests.ndjson",
}

// PackAlsoIn are the pack's other files that hold objects of a kind, which
// a row naming the kind cites too: the templates in force are PolicyGates
// and ChangeRequests of their own files, and the AuditEvents are written
// twice, the second time as OCSF.
var PackAlsoIn = map[string][]string{
	"AuditEvent":    {"auditevents.ocsf.ndjson"},
	"PolicyGate":    {"policygate-templates.ndjson"},
	"ChangeRequest": {"changerequest-templates.ndjson"},
}

// PackEvidence is a row of controls.json: the evidence with the pack files
// that hold it.
type PackEvidence struct {
	Evidence
	// Files are the pack's files of the row's kinds.
	Files []string `json:"files,omitempty"`
}

// PackControl is a control of controls.json.
type PackControl struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Requirement string         `json:"requirement"`
	Evidence    []PackEvidence `json:"evidence"`
	Customer    string         `json:"customer"`
}

// PackFramework is a framework of controls.json.
type PackFramework struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Caveat   string        `json:"caveat,omitempty"`
	Controls []PackControl `json:"controls"`
}

// ForPack is the mapping as the pack's controls.json writes it: each row
// with the files that hold its evidence. Planned rows stay, with their
// status, so the pack says what it does not evidence yet.
func ForPack(frameworks []Framework) []PackFramework {
	out := make([]PackFramework, 0, len(frameworks))
	for _, f := range frameworks {
		pf := PackFramework{ID: f.ID, Name: f.Name, Caveat: f.Caveat}
		for _, c := range f.Controls {
			pc := PackControl{ID: c.ID, Title: c.Title, Requirement: c.Requirement, Customer: c.Customer}
			for _, e := range c.Evidence {
				pe := PackEvidence{Evidence: e}
				for _, k := range e.Kinds {
					pe.Files = append(pe.Files, PackFiles[k])
					pe.Files = append(pe.Files, PackAlsoIn[k]...)
				}
				pc.Evidence = append(pc.Evidence, pe)
			}
			pf.Controls = append(pf.Controls, pc)
		}
		out = append(out, pf)
	}
	return out
}
