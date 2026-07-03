package cursorhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// Span is our own intermediate representation -- built by hook.go, then
// encoded to OTLP protobuf here. Using the wire format directly (rather than
// the full otel-go SDK) is deliberate: like the Python reference, we need to
// inject externally-computed trace/span IDs (deterministic or linked from a
// prior process), which the SDK's APIs don't cleanly support either.
type Span struct {
	TraceID       [16]byte
	SpanID        [8]byte
	ParentSpanID  *[8]byte
	Name          string
	Kind          tracepb.Span_SpanKind
	StartTime     time.Time
	EndTime       time.Time
	Attributes    map[string]interface{}
	StatusCode    tracepb.Status_StatusCode
	StatusMessage string
}

func NewSpanID() [8]byte {
	var id [8]byte
	_, _ = rand.Read(id[:])
	return id
}

func anyValue(v interface{}) *commonpb.AnyValue {
	switch t := v.(type) {
	case bool:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: t}}
	case int:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(t)}}
	case int64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: t}}
	case float64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: t}}
	default:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprintf("%v", v)}}
	}
}

func encodeAttributes(attrs map[string]interface{}) []*commonpb.KeyValue {
	kvs := make([]*commonpb.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		kvs = append(kvs, &commonpb.KeyValue{Key: k, Value: anyValue(v)})
	}
	return kvs
}

func buildRequest(span Span, serviceName string) *collectortracepb.ExportTraceServiceRequest {
	pbSpan := &tracepb.Span{
		TraceId:           span.TraceID[:],
		SpanId:            span.SpanID[:],
		Name:              span.Name,
		Kind:              span.Kind,
		StartTimeUnixNano: uint64(span.StartTime.UnixNano()),
		EndTimeUnixNano:   uint64(span.EndTime.UnixNano()),
		Attributes:        encodeAttributes(span.Attributes),
		Status: &tracepb.Status{
			Code:    span.StatusCode,
			Message: span.StatusMessage,
		},
	}
	if span.ParentSpanID != nil {
		pbSpan.ParentSpanId = span.ParentSpanID[:]
	}

	return &collectortracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "service.name", Value: anyValue(serviceName)},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Scope: &commonpb.InstrumentationScope{Name: "agentobs-cursor-hook"},
						Spans: []*tracepb.Span{pbSpan},
					},
				},
			},
		},
	}
}

// Export sends span to cfg.Endpoint using cfg.Protocol ("grpc" or "http/protobuf").
func Export(cfg Config, span Span) error {
	req := buildRequest(span, cfg.ServiceName)

	if cfg.Protocol == "http/protobuf" {
		return exportHTTP(cfg, req)
	}
	return exportGRPC(cfg, req)
}

func exportGRPC(cfg Config, req *collectortracepb.ExportTraceServiceRequest) error {
	var creds credentials.TransportCredentials
	if cfg.Insecure {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(nil)
	}

	// grpc.NewClient wants a bare host:port target, not a URL -- the default
	// endpoint everywhere else in this project is "http://host:port" (Claude
	// Code and Gemini CLI parse that themselves), so strip the scheme here.
	target := strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "https://"), "http://")

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("dialing %s: %w", target, err)
	}
	defer conn.Close()

	client := collectortracepb.NewTraceServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Timeout)*time.Second)
	defer cancel()

	_, err = client.Export(ctx, req)
	if err != nil {
		return fmt.Errorf("exporting spans via grpc: %w", err)
	}
	return nil
}

func exportHTTP(cfg Config, req *collectortracepb.ExportTraceServiceRequest) error {
	body, err := proto.Marshal(req)
	if err != nil {
		return err
	}

	endpoint := cfg.Endpoint
	if len(endpoint) < 10 || endpoint[len(endpoint)-10:] != "/v1/traces" {
		if endpoint[len(endpoint)-1] == '/' {
			endpoint += "v1/traces"
		} else {
			endpoint += "/v1/traces"
		}
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	for k, v := range cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	client := http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("posting spans to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("export failed: HTTP %d", resp.StatusCode)
	}
	return nil
}
