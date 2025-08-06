# gRPC Tracing Integration

This package provides OpenTelemetry tracing integration for gRPC calls in the accelerated-container-image project.

## Features

- **Automatic trace propagation**: Trace contexts are automatically propagated between client and server calls
- **Comprehensive tracing**: Both client and server interceptors capture request/response metadata
- **OpenTelemetry integration**: Uses standard OpenTelemetry libraries for compatibility
- **Error handling**: Automatically records errors and sets appropriate span status

## Usage

### Server Setup

The main snapshotter service already includes tracing interceptors:

```go
// Chain interceptors: tracing first, then request ID
interceptors := grpc.ChainUnaryInterceptor(
    tracing.UnaryServerInterceptor(),
    requestIDInterceptor,
)
srv := grpc.NewServer(interceptors)
```

Or use the convenience function:

```go
srv := grpc.NewServer(tracing.WithServerTracing())
```

### Client Setup

For gRPC clients, add the tracing interceptor:

```go
conn, err := grpc.DialContext(ctx, target,
    grpc.WithUnaryInterceptor(tracing.UnaryClientInterceptor()),
    // other options...
)
```

Or use the convenience function:

```go
conn, err := grpc.DialContext(ctx, target,
    tracing.WithClientTracing(),
    // other options...
)
```

### Trace Propagation

Trace IDs automatically propagate from:
1. **Incoming requests** → Server-side processing
2. **Server-side processing** → Outgoing client calls
3. **Client calls** → Downstream services

Example flow:
```
Client Request [trace-id: abc123]
    ↓
gRPC Server Interceptor [trace-id: abc123]
    ↓
Snapshotter Service [trace-id: abc123]
    ↓
Outgoing Client Call [trace-id: abc123]
```

## Trace Attributes

The interceptors automatically add these attributes to spans:

### Common Attributes
- `rpc.system`: "grpc"
- `rpc.service`: Service name (e.g., "containerd.services.snapshots.v1.Snapshots")
- `rpc.method`: Method name (e.g., "Prepare")
- `rpc.grpc.status_code`: gRPC status code
- `rpc.grpc.duration_ms`: Request duration in milliseconds

### Error Handling
- Span status set to ERROR for failed requests
- Error details recorded in span
- gRPC status code captured

## Configuration

Set these environment variables for tracing:

```bash
# Service name for traces
export OTEL_SERVICE_NAME="accelerated-container-image"

# OTLP endpoint (e.g., Jaeger collector)
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4317"

# Environment tag
export ENVIRONMENT="production"
```

## Baggage API Usage

The implementation includes full support for OpenTelemetry Baggage, allowing you to propagate key-value pairs across service boundaries.

### Setting Baggage Values

```go
import "github.com/containerd/accelerated-container-image/pkg/tracing"

// Set individual values
ctx = tracing.SetRequestID(ctx, "req-12345")
ctx = tracing.SetUserID(ctx, "user-67890")
ctx = tracing.SetOperation(ctx, "prepare-snapshot")

// Set image information
ctx = tracing.SetImageInfo(ctx, "alpine", "3.18")

// Set multiple values at once
baggageMap := map[string]string{
    "environment":  "production",
    "namespace":    "default",
    "container.id": "container-123",
}
ctx = tracing.SetBaggageFromMap(ctx, baggageMap)
```

### Getting Baggage Values

```go
// Get individual values
requestID := tracing.GetRequestID(ctx)
userID := tracing.GetUserID(ctx)
operation := tracing.GetOperation(ctx)

// Get image information
imageName, imageTag := tracing.GetImageInfo(ctx)

// Get all baggage as a map
allBaggage := tracing.GetBaggageAsMap(ctx)
```

### Automatic Baggage Propagation

Baggage values are automatically:
1. **Extracted** from incoming gRPC metadata
2. **Injected** into outgoing gRPC metadata
3. **Added as span attributes** with `baggage.` prefix
4. **Propagated** through the entire request chain

### Common Baggage Keys

The package defines standard baggage keys:
- `request.id` - Request identifier
- `user.id` - User identifier  
- `session.id` - Session identifier
- `operation` - Operation name
- `image.name` - Container image name
- `image.tag` - Container image tag
- `snapshot.key` - Snapshot key
- `container.id` - Container identifier
- `namespace` - Kubernetes namespace
- `environment` - Environment (prod, staging, etc.)

## Integration with Request ID

The request ID interceptor now uses baggage for propagation:

```go
// Check if request ID exists in baggage (from upstream)
requestID := tracing.GetRequestID(ctx)
if requestID == "" {
    // Generate new ID if not present
    requestID = generateRequestID()
    ctx = tracing.SetRequestID(ctx, requestID)
}
```

This ensures request IDs propagate across service boundaries while maintaining backward compatibility.

## Integration with Existing Code

The tracing is integrated with the existing snapshotter service:

1. **Initialization**: `tracing.InitTracer()` sets up OpenTelemetry with baggage propagation
2. **Server interceptor**: Added to gRPC server chain, extracts baggage from metadata
3. **Service wrapper**: `tracing.WithTracing()` wraps the snapshotter service
4. **Client interceptor**: Added to any outgoing gRPC clients, injects baggage into metadata
5. **Baggage utilities**: Helper functions for common baggage operations

This provides comprehensive tracing coverage at both the transport (gRPC) and application (snapshotter operations) levels, with full context propagation via baggage.