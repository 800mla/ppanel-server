package svc

import "strings"

const (
	DeviceAuthDisabledMsg      = "Device login is disabled"
	DeviceAuthSecretMissingMsg = "Device auth is unavailable: security secret is not configured"
	DeviceAuthInvalidConfigMsg = "Device auth is unavailable: device configuration is invalid"
)

func (s *ServiceContext) DeviceAuthReason() string {
	if s == nil {
		return DeviceAuthInvalidConfigMsg
	}
	if s.DeviceUnavailableReason != "" {
		return s.DeviceUnavailableReason
	}
	if s.Config.Device.Enable && strings.TrimSpace(s.Config.Device.SecuritySecret) == "" {
		return DeviceAuthSecretMissingMsg
	}
	return ""
}

func (s *ServiceContext) DeviceAuthEnabled() bool {
	return s != nil && s.Config.Device.Enable
}

func (s *ServiceContext) DeviceAuthAvailable() bool {
	return s.DeviceAuthEnabled() && s.DeviceAuthReason() == ""
}

func (s *ServiceContext) DeviceAuthDisabledReason() string {
	if reason := s.DeviceAuthReason(); reason != "" {
		return reason
	}
	return DeviceAuthDisabledMsg
}
