package main

import "net/http"

// ABI and schema versions understood by current CLIProxyAPI plugin hosts.
// Schema 6 keeps management JSON bodies unescaped.
const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 6
)

const (
	methodPluginRegister     = "plugin.register"
	methodPluginReconfigure  = "plugin.reconfigure"
	methodPluginQuiesce      = "plugin.quiesce"
	methodPluginShutdown     = "plugin.shutdown"
	methodResponseIntercept  = "response.intercept_after"
	methodManagementRegister = "management.register"
	methodManagementHandle   = "management.handle"
	methodHostHTTPDo         = "host.http.do"
	methodHostLog            = "host.log"
	configFieldTypeBoolean   = "boolean"
)

type pluginMetadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []configField `json:"ConfigFields,omitempty"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description,omitempty"`
}

type responseInterceptRequest struct {
	Body           []byte `json:"Body,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type responseInterceptResponse struct {
	Body []byte `json:"Body,omitempty"`
}

type managementRequest struct {
	Method string `json:"Method"`
	Path   string `json:"Path"`
	Body   []byte `json:"Body,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}
