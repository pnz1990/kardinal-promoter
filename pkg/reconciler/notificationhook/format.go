// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"encoding/json"
	"fmt"
	"strings"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// eventStyle is how the slack and teams formats present an event type.
type eventStyle struct {
	title string
	// emoji is a Slack shortcode for the header.
	emoji string
	// color is the Adaptive Card TextBlock color of the title.
	color string
}

var eventStyles = map[v1alpha1.NotificationHookEventType]eventStyle{
	v1alpha1.NotificationEventBundleVerified:                  {"Bundle verified", ":white_check_mark:", "Good"},
	v1alpha1.NotificationEventBundleFailed:                    {"Bundle failed", ":x:", "Attention"},
	v1alpha1.NotificationEventBundleSuperseded:                {"Bundle superseded", ":fast_forward:", "Default"},
	v1alpha1.NotificationEventBundleRollbackStarted:           {"Rollback started", ":rewind:", "Warning"},
	v1alpha1.NotificationEventBundleRolledBack:                {"Rollback complete", ":leftwards_arrow_with_hook:", "Good"},
	v1alpha1.NotificationEventPolicyGateBlocked:               {"Promotion blocked by a policy gate", ":no_entry:", "Warning"},
	v1alpha1.NotificationEventPolicyGateUnblocked:             {"Policy gate no longer blocking", ":large_green_circle:", "Good"},
	v1alpha1.NotificationEventPromotionStepFailed:             {"Promotion step failed", ":x:", "Attention"},
	v1alpha1.NotificationEventPromotionStepPROpened:           {"Promotion pull request opened", ":arrow_heading_up:", "Accent"},
	v1alpha1.NotificationEventPromotionStepWaitingForApproval: {"Promotion waiting for approval", ":eyes:", "Warning"},
}

func styleOf(event string) eventStyle {
	if s, ok := eventStyles[v1alpha1.NotificationHookEventType(event)]; ok {
		return s
	}
	return eventStyle{title: event, emoji: ":bell:", color: "Default"}
}

// fact is one labelled value of an event (pipeline, bundle, environment).
type fact struct{ label, value string }

func eventFacts(d *TemplateData) []fact {
	var out []fact
	for _, f := range []fact{{"Pipeline", d.Pipeline}, {"Bundle", d.Bundle}, {"Environment", d.Environment}} {
		if f.value != "" {
			out = append(out, f)
		}
	}
	return out
}

// Slack Block Kit limits (https://api.slack.com/reference/block-kit/blocks).
const (
	slackHeaderMax  = 150
	slackSectionMax = 3000
	slackFieldMax   = 2000
	slackTextMax    = 3000
	slackURLMax     = 3000
)

// slackEscape escapes the three characters Slack mrkdwn treats as control
// characters (https://api.slack.com/reference/surfaces/formatting#escaping).
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

type slackText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

type slackElement struct {
	Type string     `json:"type"`
	Text *slackText `json:"text,omitempty"`
	URL  string     `json:"url,omitempty"`
}

type slackBlock struct {
	Type     string        `json:"type"`
	Text     *slackText    `json:"text,omitempty"`
	Fields   []slackText   `json:"fields,omitempty"`
	Elements []interface{} `json:"elements,omitempty"`
}

type slackMessage struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

// slackBody is a Slack incoming-webhook message: a fallback text (used in
// notifications) and Block Kit blocks with the title, the message, the
// event's pipeline, bundle and environment, a button to the PR when there
// is one, and the event key.
func slackBody(d *TemplateData) ([]byte, error) {
	st := styleOf(d.Event)
	msg := slackEscape.Replace(d.Message)
	blocks := []slackBlock{
		{Type: "header", Text: &slackText{Type: "plain_text", Emoji: true,
			Text: truncateRunes(slackHeaderMax, st.emoji+" "+st.title)}},
		{Type: "section", Text: &slackText{Type: "mrkdwn", Text: truncateRunes(slackSectionMax, msg)}},
	}
	if facts := eventFacts(d); len(facts) > 0 {
		fields := make([]slackText, 0, len(facts))
		for _, f := range facts {
			fields = append(fields, slackText{Type: "mrkdwn",
				Text: truncateRunes(slackFieldMax, "*"+f.label+"*\n"+slackEscape.Replace(f.value))})
		}
		blocks = append(blocks, slackBlock{Type: "section", Fields: fields})
	}
	if d.PRURL != "" && len(d.PRURL) <= slackURLMax {
		blocks = append(blocks, slackBlock{Type: "actions", Elements: []interface{}{
			slackElement{Type: "button", Text: &slackText{Type: "plain_text", Text: "Open pull request"}, URL: d.PRURL},
		}})
	}
	blocks = append(blocks, slackBlock{Type: "context", Elements: []interface{}{
		slackText{Type: "mrkdwn", Text: truncateRunes(slackFieldMax,
			slackEscape.Replace(fmt.Sprintf("kardinal · %s · %s", d.Key, d.Timestamp)))},
	}})
	body, err := json.Marshal(slackMessage{
		Text:   truncateRunes(slackTextMax, st.title+": "+d.Message),
		Blocks: blocks,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal slack message: %w", err)
	}
	return body, nil
}

// Adaptive Card shapes for a Teams Workflows webhook
// ("When a Teams webhook request is received"), which takes a message with
// the card as an attachment.
type teamsMessage struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string       `json:"contentType"`
	ContentURL  *string      `json:"contentUrl"`
	Content     adaptiveCard `json:"content"`
}

type adaptiveCard struct {
	Schema  string        `json:"$schema"`
	Type    string        `json:"type"`
	Version string        `json:"version"`
	Body    []interface{} `json:"body"`
	Actions []cardAction  `json:"actions,omitempty"`
	MSTeams *cardMSTeams  `json:"msteams,omitempty"`
}

type cardMSTeams struct {
	Width string `json:"width"`
}

type cardTextBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Wrap     bool   `json:"wrap"`
	Weight   string `json:"weight,omitempty"`
	Size     string `json:"size,omitempty"`
	Color    string `json:"color,omitempty"`
	IsSubtle bool   `json:"isSubtle,omitempty"`
}

type cardFactSet struct {
	Type  string     `json:"type"`
	Facts []cardFact `json:"facts"`
}

type cardFact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

type cardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// teamsBody is a Teams Workflows webhook message carrying an Adaptive Card
// (version 1.4): the title, the message, a fact set, a button to the PR when
// there is one, and the event key.
func teamsBody(d *TemplateData) ([]byte, error) {
	st := styleOf(d.Event)
	body := []interface{}{
		cardTextBlock{Type: "TextBlock", Text: st.title, Wrap: true, Weight: "Bolder", Size: "Medium", Color: st.color},
		cardTextBlock{Type: "TextBlock", Text: d.Message, Wrap: true},
	}
	if facts := eventFacts(d); len(facts) > 0 {
		fs := cardFactSet{Type: "FactSet"}
		for _, f := range facts {
			fs.Facts = append(fs.Facts, cardFact{Title: f.label, Value: f.value})
		}
		body = append(body, fs)
	}
	body = append(body, cardTextBlock{Type: "TextBlock", Text: fmt.Sprintf("kardinal · %s · %s", d.Key, d.Timestamp),
		Wrap: true, Size: "Small", IsSubtle: true})
	card := adaptiveCard{
		Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
		Type:    "AdaptiveCard",
		Version: "1.4",
		Body:    body,
		MSTeams: &cardMSTeams{Width: "Full"},
	}
	if d.PRURL != "" {
		card.Actions = []cardAction{{Type: "Action.OpenUrl", Title: "Open pull request", URL: d.PRURL}}
	}
	out, err := json.Marshal(teamsMessage{
		Type: "message",
		Attachments: []teamsAttachment{{
			ContentType: "application/vnd.microsoft.card.adaptive",
			Content:     card,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal teams message: %w", err)
	}
	return out, nil
}
