package automation

import "fmt"

const (
	DeliveryKindSession = "session"
	DeliveryKindChannel = "channel"
)

// DeliveryTarget describes where one automation result should be delivered.
type DeliveryTarget struct {
	Kind       string            `json:"kind,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
	Channel    string            `json:"channel,omitempty"`
	SessionKey string            `json:"session_key,omitempty"`
	Recipient  string            `json:"recipient,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Clone returns a detached copy of the delivery target metadata.
func (t DeliveryTarget) Clone() DeliveryTarget {
	cloned := t
	if len(t.Metadata) > 0 {
		cloned.Metadata = make(map[string]string, len(t.Metadata))
		for key, value := range t.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return cloned
}

// IsZero reports whether the target has no usable routing information.
func (t DeliveryTarget) IsZero() bool {
	return t.Kind == "" && t.SessionID == "" && t.Channel == "" && t.SessionKey == "" && t.Recipient == "" && len(t.Metadata) == 0
}

const (
	DeliveryErrorBlocked   = "delivery_blocked"
	DeliveryErrorTemporary = "delivery_temporary"
)

// DeliveryError classifies automation delivery failures.
type DeliveryError struct {
	Code    string
	Message string
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("delivery error: %s", e.Code)
}

// NewBlockedError marks one delivery as blocked until external context is restored.
func NewBlockedError(message string) error {
	return &DeliveryError{Code: DeliveryErrorBlocked, Message: message}
}

// IsBlockedError reports whether err is a blocked delivery condition.
func IsBlockedError(err error) bool {
	deliveryErr, ok := err.(*DeliveryError)
	return ok && deliveryErr.Code == DeliveryErrorBlocked
}

// SessionContext captures the current runtime session metadata for tool calls.
type SessionContext struct {
	SessionID       string            `json:"session_id,omitempty"`
	LocatorChannel  string            `json:"locator_channel,omitempty"`
	LocatorKey      string            `json:"locator_key,omitempty"`
	LocatorUserID   string            `json:"locator_user_id,omitempty"`
	Source          string            `json:"source,omitempty"`
	Sender          string            `json:"sender,omitempty"`
	AgentProfile    string            `json:"agent_profile,omitempty"`
	SecurityProfile string            `json:"security_profile,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ProjectLedger   string            `json:"project_ledger,omitempty"`
	DefaultDelivery DeliveryTarget    `json:"default_delivery,omitempty"`
}

// Clone returns a detached copy of the runtime session context.
func (c SessionContext) Clone() SessionContext {
	cloned := c
	if len(c.Metadata) > 0 {
		cloned.Metadata = make(map[string]string, len(c.Metadata))
		for key, value := range c.Metadata {
			cloned.Metadata[key] = value
		}
	}
	cloned.DefaultDelivery = c.DefaultDelivery.Clone()
	return cloned
}
