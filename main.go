package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
	"gopkg.in/yaml.v3"
)

const (
	pluginID      = "opencode-go-oauth"
	providerID    = "opencode-go"
	pluginName    = "OpenCode Go"
	pluginVersion = "0.1.11"
	pluginAuthor  = "esauvisky"
	pluginRepo    = "https://github.com/esauvisky/CLIProxyAPI-OpenCode-Go-Plugin"
	pluginLogo    = "https://raw.githubusercontent.com/esauvisky/CLIProxyAPI-OpenCode-Go-Plugin/main/assets/logo.png"

	defaultConsoleURL        = "https://opencode.ai/console"
	defaultInferenceBaseURL  = "https://opencode.ai/inference/go"
	defaultOAuthClientID     = "opencode-cli"
	defaultSessionHeader     = "cpa-main"
	defaultUserAgent         = "opencode/1.2.0"
	defaultGoAllowance5h     = 12.0
	defaultGoAllowanceWeekly = 30.0
	defaultGoAllowanceMonth  = 60.0
	defaultGoPlus5h          = 48.0
	defaultGoPlusWeekly      = 120.0
	defaultGoPlusMonth       = 240.0
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type statusError struct {
	code       string
	message    string
	httpStatus int
}

func (e statusError) Error() string   { return e.message }
func (e statusError) StatusCode() int { return e.httpStatus }

type pluginConfig struct {
	Enabled          bool    `yaml:"enabled" json:"enabled"`
	Priority         int     `yaml:"priority" json:"priority"`
	SessionID        string  `yaml:"session_id" json:"session_id"`
	UserAgent        string  `yaml:"user_agent" json:"user_agent"`
	ConsoleURL       string  `yaml:"console_url" json:"console_url"`
	InferenceBaseURL string  `yaml:"inference_base_url" json:"inference_base_url"`
	Plan             string  `yaml:"plan" json:"plan"`
	Allowance5h      float64 `yaml:"allowance_5h" json:"allowance_5h"`
	AllowanceWeekly  float64 `yaml:"allowance_weekly" json:"allowance_weekly"`
	AllowanceMonthly float64 `yaml:"allowance_monthly" json:"allowance_monthly"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
	QuotaProvider         bool                         `json:"quota_provider"`
}

var activeConfig atomic.Value

func init() {
	activeConfig.Store(defaultPluginConfig())
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:          true,
		Priority:         10,
		SessionID:        defaultSessionHeader,
		UserAgent:        defaultUserAgent,
		ConsoleURL:       defaultConsoleURL,
		InferenceBaseURL: defaultInferenceBaseURL,
		Plan:             "go",
	}
}

func currentConfig() pluginConfig {
	if cfg, ok := activeConfig.Load().(pluginConfig); ok {
		return cfg
	}
	return defaultPluginConfig()
}

func configurePlugin(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := defaultPluginConfig()
	if len(req.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &cfg); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg.SessionID = firstNonEmpty(cfg.SessionID, defaultSessionHeader)
	cfg.UserAgent = firstNonEmpty(cfg.UserAgent, defaultUserAgent)
	cfg.ConsoleURL = strings.TrimRight(firstNonEmpty(cfg.ConsoleURL, defaultConsoleURL), "/")
	cfg.InferenceBaseURL = strings.TrimRight(firstNonEmpty(cfg.InferenceBaseURL, defaultInferenceBaseURL), "/")
	cfg.Plan = strings.ToLower(firstNonEmpty(cfg.Plan, "go"))
	activeConfig.Store(cfg)
	return nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepo,
			Logo:             pluginLogo,
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "session_id",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     defaultSessionHeader,
					Description: "Default x-opencode-session header sent when downstream request has no session ID.",
				},
				{
					Name:        "plan",
					Type:        pluginapi.ConfigFieldTypeEnum,
					Default:     "go",
					EnumValues:  []string{"go", "go-plus"},
					Description: "Subscription tier for allowance bucket calculations: go ($10/mo, $12/$30/$60) or go-plus ($40/mo, $48/$120/$240).",
				},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:  []string{"openai", "claude", "codex", "openai-response", "gemini"},
			ExecutorOutputFormats: []string{"openai", "claude", "codex", "openai-response", "gemini"},
			QuotaProvider:         true,
		},
	}
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		status := 0
		var sc interface{ StatusCode() int }
		if errors.As(errHandle, &sc) && sc != nil {
			status = sc.StatusCode()
		}
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), status))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errCfg := configurePlugin(request); errCfg != nil {
			return nil, errCfg
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return okEnvelope(struct{}{})

	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(map[string]string{"identifier": providerID})
	case pluginabi.MethodAuthParse:
		return handleAuthParse(request)
	case pluginabi.MethodAuthLoginStart:
		return handleAuthLoginStart(request)
	case pluginabi.MethodAuthLoginPoll:
		return handleAuthLoginPoll(request)
	case pluginabi.MethodAuthRefresh:
		return handleAuthRefresh(request)

	case pluginabi.MethodModelStatic:
		return handleModelStatic(request)
	case pluginabi.MethodModelForAuth:
		return handleModelForAuth(request)

	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": providerID})
	case pluginabi.MethodExecutorExecute:
		return handleExecutorExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecutorExecuteStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return handleExecutorCountTokens(request)
	case pluginabi.MethodExecutorHTTPRequest:
		return handleExecutorHTTPRequest(request)

	case pluginabi.MethodQuotaIdentifier:
		return okEnvelope(map[string]string{"identifier": providerID})
	case pluginabi.MethodQuotaDescribe:
		return handleQuotaDescribe(request)
	case pluginabi.MethodQuotaFetch:
		return handleQuotaFetch(request)
	case pluginabi.MethodQuotaReset:
		return handleQuotaReset(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

var hostCallHook func(method string, payload any) (json.RawMessage, error)

func callHost(method string, payload any) (json.RawMessage, error) {
	if hostCallHook != nil {
		return hostCallHook(method, payload)
	}
	rawPayload, errMarshal := marshalJSONNoEscape(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response (code=%d)", method, int(callCode))
	}

	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, statusError{
				code:       env.Error.Code,
				message:    env.Error.Message,
				httpStatus: env.Error.HTTPStatus,
			}
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := marshalJSONNoEscape(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return marshalJSONNoEscape(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, httpStatus int) []byte {
	raw, _ := marshalJSONNoEscape(envelope{
		OK: false,
		Error: &envelopeError{
			Code:       code,
			Message:    message,
			HTTPStatus: httpStatus,
		},
	})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func marshalJSONNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
