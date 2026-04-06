package managementasset

import _ "embed"

//go:embed static/management-shell.html
var builtinManagementShellHTML string

//go:embed static/management-auth.html
var builtinAuthManagementHTML string

//go:embed static/management-logs.html
var builtinLogsManagementHTML string

// BuiltinManagementShellHTML returns the embedded management shell page.
func BuiltinManagementShellHTML() string {
	return builtinManagementShellHTML
}

// BuiltinAuthManagementHTML returns the embedded auth/log management page.
func BuiltinAuthManagementHTML() string {
	return builtinAuthManagementHTML
}

// BuiltinLogsManagementHTML returns the embedded logs-only management page.
func BuiltinLogsManagementHTML() string {
	return builtinLogsManagementHTML
}
