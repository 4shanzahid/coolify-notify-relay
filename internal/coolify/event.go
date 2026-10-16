// Package coolify models Coolify's webhook notifications and its read-only REST API.
package coolify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Event names emitted by Coolify v4.4.x on the webhook channel that the relay
// treats specially. Every other event is still routable by name.
const (
	EventDeploymentSuccess = "deployment_success"
	EventDeploymentFailed  = "deployment_failed"
	EventTest              = "test"
)

// knownEvents are the webhook event names Coolify v4.4.x emits.
var knownEvents = map[string]bool{
	"api_token_expiring": true, "backup_failed": true, "backup_missing": true, "backup_success": true,
	"container_restarted": true, "deployment_failed": true, "deployment_success": true,
	"docker_cleanup_failed": true, "docker_cleanup_success": true, "general": true,
	"hetzner_deletion_failed": true, "high_disk_usage": true, "restart_limit_reached": true,
	"server_force_disabled": true, "server_force_enabled": true, "server_patch_check": true,
	"server_patch_check_error": true, "server_reachable": true, "server_unreachable": true,
	"ssl_certificate_renewal": true, "status_changed": true, "task_failed": true, "task_success": true,
	"test": true, "traefik_version_outdated": true, "volume_backup_failed": true,
	"volume_backup_missing": true, "volume_backup_recovery_failed": true, "volume_backup_success": true,
}

// MetricLabel maps an event name to a bounded label value, so arbitrary
// request bodies cannot grow metric cardinality.
func MetricLabel(event string) string {
	if knownEvents[event] {
		return event
	}
	return "other"
}

// Event is a Coolify webhook notification. Known fields are typed; the full
// payload is kept in Raw so templates can show fields of any event type.
type Event struct {
	Event           string  `json:"event"`
	Success         bool    `json:"success"`
	Message         string  `json:"message"`
	ApplicationName string  `json:"application_name"`
	ApplicationUUID string  `json:"application_uuid"`
	DeploymentUUID  string  `json:"deployment_uuid"`
	DeploymentURL   string  `json:"deployment_url"`
	Project         string  `json:"project"`
	Environment     string  `json:"environment"`
	FQDN            string  `json:"fqdn"`
	PullRequestID   FlexInt `json:"pull_request_id"`
	PreviewFQDN     string  `json:"preview_fqdn"`
	URL             string  `json:"url"`

	Raw map[string]any `json:"-"`

	dedupeKey string
}

// Parse decodes a webhook body. Unknown fields are kept in Raw, never rejected,
// so a Coolify upgrade that adds fields does not break the relay.
func Parse(body []byte) (*Event, error) {
	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &e.Raw); err != nil {
		return nil, err
	}
	if e.Event == "" {
		return nil, errors.New("missing event field")
	}
	if e.DeploymentUUID != "" {
		e.dedupeKey = e.Event + "|" + e.DeploymentUUID
	} else {
		sum := sha256.Sum256(body)
		e.dedupeKey = e.Event + "|" + hex.EncodeToString(sum[:])
	}
	return &e, nil
}

// DedupeKey identifies a delivery so Coolify's connection-level retries are not
// relayed twice.
func (e *Event) DedupeKey() string { return e.dedupeKey }

func (e *Event) IsDeployment() bool {
	return e.Event == EventDeploymentSuccess || e.Event == EventDeploymentFailed
}

func (e *Event) IsPreview() bool { return e.PullRequestID > 0 }

// ShortAppName strips Coolify's ":<branch>-<uuid>" suffix, e.g.
// "shop-api:dev-a1b2c3…" → "shop-api".
func (e *Event) ShortAppName() string {
	name := e.ApplicationName
	if i := strings.LastIndex(name, ":"); i > 0 && (e.ApplicationUUID == "" || strings.HasSuffix(name, e.ApplicationUUID)) {
		return name[:i]
	}
	return name
}

// AppURL is the public URL of what was deployed (the preview URL for PR previews).
func (e *Event) AppURL() string {
	if e.IsPreview() && e.PreviewFQDN != "" {
		return e.PreviewFQDN
	}
	return e.FQDN
}

// String returns a string field from the raw payload, or "".
func (e *Event) String(key string) string {
	switch v := e.Raw[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// FlexInt accepts a JSON number, numeric string or null.
type FlexInt int

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = FlexInt(n)
	return nil
}
