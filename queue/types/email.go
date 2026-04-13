package types

const (
	// ForthwithSendEmail forthwith send email
	ForthwithSendEmail = "forthwith:email:send"
)

const (
	EmailTypeVerify        = "verify"
	EmailTypeMaintenance   = "maintenance"
	EmailTypeExpiration    = "expiration"
	EmailTypeTrafficExceed = "traffic_exceed"
	EmailTypeCustom        = "custom"
)

type (
	SendEmailPayload struct {
		Type    string                 `json:"type"`
		Email   string                 `json:"to"`
		Subject string                 `json:"subject"`
		Content map[string]interface{} `json:"content"`
		Source  string                 `json:"source,omitempty"`
		LogID   int64                  `json:"log_id,omitempty"`
		TraceID string                 `json:"trace_id,omitempty"`
	}
)
