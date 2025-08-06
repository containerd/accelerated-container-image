/*
   Copyright The Accelerated Container Image Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package tracing

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const grpcTracerName = "accelerated-container-image/grpc"

// UnaryServerInterceptor returns a grpc.UnaryServerInterceptor that traces incoming gRPC calls
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	tracer := otel.GetTracerProvider().Tracer(grpcTracerName)

	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Extract trace context and baggage from incoming metadata
		ctx = extractTraceAndBaggageFromMetadata(ctx)

		// Start span with baggage attributes
		spanAttrs := []attribute.KeyValue{
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", extractServiceFromMethod(info.FullMethod)),
			attribute.String("rpc.method", extractMethodFromFullMethod(info.FullMethod)),
			attribute.String("rpc.grpc.status_code", "OK"), // Will be updated if error occurs
		}

		// Add baggage values as span attributes
		spanAttrs = append(spanAttrs, baggageToAttributes(baggage.FromContext(ctx))...)

		ctx, span := tracer.Start(ctx, info.FullMethod,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(spanAttrs...),
		)
		defer span.End()

		// Record request start time
		startTime := time.Now()

		// Call the handler
		resp, err := handler(ctx, req)

		// Record duration
		duration := time.Since(startTime)
		span.SetAttributes(attribute.Int64("rpc.grpc.duration_ms", duration.Milliseconds()))

		// Handle error
		if err != nil {
			s, _ := status.FromError(err)
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(attribute.String("rpc.grpc.status_code", s.Code().String()))
			span.RecordError(err)
		}

		return resp, err
	}
}

// UnaryClientInterceptor returns a grpc.UnaryClientInterceptor that traces outgoing gRPC calls
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	tracer := otel.GetTracerProvider().Tracer(grpcTracerName)

	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Start span with baggage attributes
		spanAttrs := []attribute.KeyValue{
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", extractServiceFromMethod(method)),
			attribute.String("rpc.method", extractMethodFromFullMethod(method)),
			attribute.String("rpc.grpc.status_code", "OK"), // Will be updated if error occurs
		}

		// Add baggage values as span attributes
		spanAttrs = append(spanAttrs, baggageToAttributes(baggage.FromContext(ctx))...)

		ctx, span := tracer.Start(ctx, method,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(spanAttrs...),
		)
		defer span.End()

		// Inject trace context and baggage into outgoing metadata
		ctx = injectTraceAndBaggageIntoMetadata(ctx)

		// Record request start time
		startTime := time.Now()

		// Make the call
		err := invoker(ctx, method, req, reply, cc, opts...)

		// Record duration
		duration := time.Since(startTime)
		span.SetAttributes(attribute.Int64("rpc.grpc.duration_ms", duration.Milliseconds()))

		// Handle error
		if err != nil {
			s, _ := status.FromError(err)
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(attribute.String("rpc.grpc.status_code", s.Code().String()))
			span.RecordError(err)
		}

		return err
	}
}

// extractTraceAndBaggageFromMetadata extracts both trace context and baggage from gRPC metadata
func extractTraceAndBaggageFromMetadata(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}

	// Use OpenTelemetry's propagator (handles both trace context and baggage)
	propagator := otel.GetTextMapPropagator()
	return propagator.Extract(ctx, &metadataCarrier{md: md})
}

// injectTraceAndBaggageIntoMetadata injects both trace context and baggage into gRPC metadata
func injectTraceAndBaggageIntoMetadata(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.New(nil)
	} else {
		md = md.Copy()
	}

	// Use OpenTelemetry's propagator (handles both trace context and baggage)
	propagator := otel.GetTextMapPropagator()
	carrier := &metadataCarrier{md: md}
	propagator.Inject(ctx, carrier)

	return metadata.NewOutgoingContext(ctx, md)
}

// metadataCarrier implements TextMapCarrier for gRPC metadata
type metadataCarrier struct {
	md metadata.MD
}

func (c *metadataCarrier) Get(key string) string {
	values := c.md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (c *metadataCarrier) Set(key, value string) {
	c.md.Set(key, value)
}

func (c *metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.md))
	for k := range c.md {
		keys = append(keys, k)
	}
	return keys
}

// extractServiceFromMethod extracts service name from full method name
// e.g., "/containerd.services.snapshots.v1.Snapshots/Prepare" -> "containerd.services.snapshots.v1.Snapshots"
func extractServiceFromMethod(fullMethod string) string {
	if len(fullMethod) == 0 {
		return ""
	}

	// Remove leading slash
	if fullMethod[0] == '/' {
		fullMethod = fullMethod[1:]
	}

	// Find last slash
	lastSlash := -1
	for i := len(fullMethod) - 1; i >= 0; i-- {
		if fullMethod[i] == '/' {
			lastSlash = i
			break
		}
	}

	if lastSlash == -1 {
		return fullMethod
	}

	return fullMethod[:lastSlash]
}

// extractMethodFromFullMethod extracts method name from full method name
// e.g., "/containerd.services.snapshots.v1.Snapshots/Prepare" -> "Prepare"
func extractMethodFromFullMethod(fullMethod string) string {
	if len(fullMethod) == 0 {
		return ""
	}

	// Find last slash
	lastSlash := -1
	for i := len(fullMethod) - 1; i >= 0; i-- {
		if fullMethod[i] == '/' {
			lastSlash = i
			break
		}
	}

	if lastSlash == -1 {
		return fullMethod
	}

	return fullMethod[lastSlash+1:]
}

// baggageToAttributes converts baggage members to span attributes with baggage. prefix
func baggageToAttributes(b baggage.Baggage) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	for _, member := range b.Members() {
		key := "baggage." + member.Key()
		attrs = append(attrs, attribute.String(key, member.Value()))
	}

	return attrs
}

// WithClientTracing returns gRPC dial options that include the tracing client interceptor
func WithClientTracing() grpc.DialOption {
	return grpc.WithUnaryInterceptor(UnaryClientInterceptor())
}

// WithServerTracing returns gRPC server options that include the tracing server interceptor
func WithServerTracing() grpc.ServerOption {
	return grpc.UnaryInterceptor(UnaryServerInterceptor())
}
