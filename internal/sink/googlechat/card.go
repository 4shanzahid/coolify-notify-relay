package googlechat

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/text"
)

// Message is the subset of the Google Chat v1 Message resource that webhooks accept.
type Message struct {
	Text string `json:"text,omitempty"`
	// FallbackText is shown where cards can't render (e.g. mobile push
	// notifications) without duplicating the card in the space.
	FallbackText string     `json:"fallbackText,omitempty"`
	CardsV2      []CardWrap `json:"cardsV2,omitempty"`
	Thread       *Thread    `json:"thread,omitempty"`
}

type Thread struct {
	ThreadKey string `json:"threadKey"`
}

type CardWrap struct {
	CardID string `json:"cardId"`
	Card   Card   `json:"card"`
}

type Card struct {
	Header   *Header   `json:"header,omitempty"`
	Sections []Section `json:"sections"`
}

type Header struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
}

type Section struct {
	Widgets []Widget `json:"widgets"`
}

type Widget struct {
	DecoratedText *DecoratedText `json:"decoratedText,omitempty"`
	TextParagraph *TextParagraph `json:"textParagraph,omitempty"`
	ButtonList    *ButtonList    `json:"buttonList,omitempty"`
}

type DecoratedText struct {
	TopLabel string `json:"topLabel,omitempty"`
	Text     string `json:"text"`
	WrapText bool   `json:"wrapText,omitempty"`
}

type TextParagraph struct {
	Text string `json:"text"`
}

type ButtonList struct {
	Buttons []Button `json:"buttons"`
}

type Button struct {
	Text    string  `json:"text"`
	OnClick OnClick `json:"onClick"`
}

type OnClick struct {
	OpenLink OpenLink `json:"openLink"`
}

type OpenLink struct {
	URL string `json:"url"`
}

// Details is optional enrichment from the Coolify API.
type Details struct {
	Commit        string
	CommitMessage string
	Repo          string // owner/repo
	Branch        string
}

const maxFieldLen = 600

// Build renders an event as a Chat message: a card with the details and links,
// plus a one-line fallbackText used for notifications.
func Build(e *coolify.Event, d *Details, now time.Time) Message {
	if e.IsDeployment() {
		return buildDeployment(e, d, now)
	}
	return buildGeneric(e, now)
}

func buildDeployment(e *coolify.Event, d *Details, now time.Time) Message {
	app := e.ShortAppName()
	ok := e.Event == coolify.EventDeploymentSuccess

	icon, verb := "✅", "deployed"
	if !ok {
		icon, verb = "❌", "deployment failed"
	}
	where := e.Environment
	if e.IsPreview() {
		where = fmt.Sprintf("%s · PR #%d", e.Environment, e.PullRequestID)
	}

	summary := fmt.Sprintf("%s %s %s (%s)", icon, app, verb, where)
	if d != nil && d.Commit != "" {
		summary += " @ " + shortSHA(d.Commit)
	}

	var widgets []Widget
	if d != nil && d.Commit != "" {
		sha := html.EscapeString(shortSHA(d.Commit))
		if d.Repo != "" && text.IsSHA(d.Commit) {
			sha = fmt.Sprintf(`<a href="https://github.com/%s/commit/%s">%s</a>`, html.EscapeString(d.Repo), d.Commit, sha)
		}
		commit := sha
		if msg := firstLine(d.CommitMessage); msg != "" {
			commit += " " + html.EscapeString(text.Truncate(msg, 200))
		}
		widgets = append(widgets, Widget{DecoratedText: &DecoratedText{TopLabel: "Commit", Text: commit, WrapText: true}})
	}
	if d != nil && d.Branch != "" {
		widgets = append(widgets, Widget{DecoratedText: &DecoratedText{TopLabel: "Branch", Text: html.EscapeString(d.Branch)}})
	}
	widgets = append(widgets,
		Widget{DecoratedText: &DecoratedText{TopLabel: "Status", Text: statusText(ok)}},
		Widget{DecoratedText: &DecoratedText{TopLabel: "Time", Text: now.Format("Mon 02 Jan 15:04 MST")}},
	)

	var buttons []Button
	if u := e.AppURL(); isHTTP(u) {
		buttons = append(buttons, link("Open app", u))
	}
	if isHTTP(e.DeploymentURL) {
		buttons = append(buttons, link("Deployment logs", e.DeploymentURL))
	}
	if len(buttons) > 0 {
		widgets = append(widgets, Widget{ButtonList: &ButtonList{Buttons: buttons}})
	}

	return Message{
		FallbackText: summary,
		CardsV2: []CardWrap{{
			CardID: "deployment",
			Card: Card{
				Header:   &Header{Title: fmt.Sprintf("%s %s", icon, app), Subtitle: fmt.Sprintf("%s · %s", e.Project, where)},
				Sections: []Section{{Widgets: widgets}},
			},
		}},
	}
}

// genericFields are payload keys worth showing for non-deployment events, in display order.
var genericFields = []struct{ key, label string }{
	{"application_name", "Application"},
	{"project", "Project"},
	{"environment", "Environment"},
	{"server_name", "Server"},
	{"container_name", "Container"},
	{"database_name", "Database"},
	{"resource_name", "Resource"},
	{"task_name", "Task"},
	{"disk_usage", "Disk usage %"},
	{"restart_count", "Restarts"},
	{"error_message", "Error"},
	{"error", "Error"},
	{"error_output", "Output"},
	{"output", "Output"},
}

func buildGeneric(e *coolify.Event, now time.Time) Message {
	icon := "⚠️"
	switch {
	case e.Event == coolify.EventTest:
		icon = "🔔"
	case e.Success:
		icon = "✅"
	}
	msg := e.Message
	if msg == "" {
		msg = e.Event
	}

	var widgets []Widget
	for _, f := range genericFields {
		v := e.String(f.key)
		if v == "" {
			continue
		}
		if f.key == "application_name" {
			v = e.ShortAppName()
		}
		widgets = append(widgets, Widget{DecoratedText: &DecoratedText{
			TopLabel: f.label, Text: html.EscapeString(text.Truncate(v, maxFieldLen)), WrapText: true,
		}})
	}
	widgets = append(widgets, Widget{DecoratedText: &DecoratedText{TopLabel: "Time", Text: now.Format("Mon 02 Jan 15:04 MST")}})
	if u := firstNonEmpty(e.URL, e.FQDN); isHTTP(u) {
		widgets = append(widgets, Widget{ButtonList: &ButtonList{Buttons: []Button{link("Open in Coolify", u)}}})
	}

	return Message{
		FallbackText: fmt.Sprintf("%s %s", icon, msg),
		CardsV2: []CardWrap{{
			CardID: "event",
			Card: Card{
				Header:   &Header{Title: fmt.Sprintf("%s %s", icon, msg), Subtitle: e.Event},
				Sections: []Section{{Widgets: widgets}},
			},
		}},
	}
}

func statusText(ok bool) string {
	if ok {
		return `<font color="#188038">Succeeded</font>`
	}
	return `<font color="#d93025">Failed</font>`
}

func link(label, u string) Button { return Button{Text: label, OnClick: OnClick{OpenLink{URL: u}}} }

func isHTTP(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

func shortSHA(s string) string {
	if len(s) > 7 && text.IsSHA(s) {
		return s[:7]
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
