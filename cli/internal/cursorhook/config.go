// Package cursorhook implements the Cursor hooks.json -> OTel trace bridge.
// Cursor has no native OTel export, so this is invoked by Cursor's own hook
// system (via ~/.cursor/hooks/otel_hook.sh) once per hook event, receiving
// the event as JSON on stdin and emitting a span for it.
package cursorhook

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Endpoint    string
	ServiceName string
	Insecure    bool
	Headers     map[string]string
	MaskPrompts bool
	Timeout     int
	Protocol    string // "grpc" or "http/protobuf"
}

func normalizeProtocol(p string) string {
	p = strings.ToLower(p)
	switch p {
	case "http":
		return "http/protobuf"
	case "grpc", "http/protobuf":
		return p
	default:
		return "grpc"
	}
}

func parseHeaders(s string) map[string]string {
	if s == "" {
		return nil
	}
	headers := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok {
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func fromEnv() Config {
	timeout, err := strconv.Atoi(os.Getenv("OTEL_EXPORTER_OTLP_TIMEOUT"))
	if err != nil {
		timeout = 30
	}
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4317"
	}
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "cursor-agent"
	}
	insecure := os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")

	return Config{
		Endpoint:    endpoint,
		ServiceName: serviceName,
		Insecure:    insecure == "" || strings.EqualFold(insecure, "true"),
		Headers:     parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
		MaskPrompts: strings.EqualFold(os.Getenv("CURSOR_OTEL_MASK_PROMPTS"), "true"),
		Timeout:     timeout,
		Protocol:    normalizeProtocol(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")),
	}
}

// jsonConfigFile mirrors the JSON config file schema: standard OTEL env var
// names as JSON keys, so the same config.json written by `agentobs connect`
// works whether it's read by this hook or manually inspected.
type jsonConfigFile struct {
	Endpoint    string      `json:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	ServiceName string      `json:"OTEL_SERVICE_NAME"`
	Protocol    string      `json:"OTEL_EXPORTER_OTLP_PROTOCOL"`
	Insecure    interface{} `json:"OTEL_EXPORTER_OTLP_INSECURE"`
	Headers     interface{} `json:"OTEL_EXPORTER_OTLP_HEADERS"`
	MaskPrompts interface{} `json:"CURSOR_OTEL_MASK_PROMPTS"`
	Timeout     interface{} `json:"OTEL_EXPORTER_OTLP_TIMEOUT"`
}

func asBool(v interface{}, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return def
	}
}

func asInt(v interface{}, def int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	}
	return def
}

func fromFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var raw jsonConfigFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}

	endpoint := raw.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:4317"
	}
	serviceName := raw.ServiceName
	if serviceName == "" {
		serviceName = "cursor-agent"
	}

	var headers map[string]string
	switch h := raw.Headers.(type) {
	case string:
		headers = parseHeaders(h)
	case map[string]interface{}:
		headers = map[string]string{}
		for k, v := range h {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}
	}

	return Config{
		Endpoint:    endpoint,
		ServiceName: serviceName,
		Insecure:    asBool(raw.Insecure, true),
		Headers:     headers,
		MaskPrompts: asBool(raw.MaskPrompts, false),
		Timeout:     asInt(raw.Timeout, 30),
		Protocol:    normalizeProtocol(raw.Protocol),
	}, nil
}

// Load reads config from configFile if given (falling back to env vars if the
// file doesn't exist), else from env vars directly -- same precedence as the
// original Python implementation.
func Load(configFile string) Config {
	if configFile != "" {
		if cfg, err := fromFile(configFile); err == nil {
			return cfg
		}
	}
	return fromEnv()
}
