// Package appidentity contains the shared Beta identity used by the native runtime.
package appidentity

const (
	AppID          = "CodexFloatingBar.Next"
	ProductName    = "CodexFloatingBar Next Beta"
	ExecutableName = AppID + ".exe"
	DataDirectory  = AppID
	WindowClass    = AppID + ".Window"
	MutexName      = "Local\\" + AppID + ".SingleInstance"
)
