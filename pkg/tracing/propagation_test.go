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

package tracing_test

import (
	"context"
	"testing"

	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	"github.com/containerd/containerd/v2/contrib/snapshotservice"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/containerd/accelerated-container-image/pkg/tracing"
)

// trackedSnapshotter wraps the mock snapshotter to capture trace context
type trackedSnapshotter struct {
	*mockSnapshotter
	capturedTraceIDs []trace.TraceID
}

func (t *trackedSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	// Capture trace ID from context
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		t.capturedTraceIDs = append(t.capturedTraceIDs, span.SpanContext().TraceID())
	}
	return t.mockSnapshotter.Prepare(ctx, key, parent, opts...)
}

func TestTraceIDPropagation(t *testing.T) {
	// Initialize test tracer
	resetTestSpans()

	// Create tracked snapshotter
	tracked := &trackedSnapshotter{
		mockSnapshotter:  &mockSnapshotter{},
		capturedTraceIDs: make([]trace.TraceID, 0),
	}

	// Wrap with service and tracing
	service := snapshotservice.FromSnapshotter(tracked)
	tracedService := tracing.WithTracing(service)

	// Setup gRPC test environment
	ctx, conn, cleanup := newGRPCTestEnv(t, tracedService)
	defer cleanup()

	client := snapshotsapi.NewSnapshotsClient(conn)

	// Create a parent trace to start with
	tracer := otel.GetTracerProvider().Tracer("test")
	parentCtx, parentSpan := tracer.Start(ctx, "parent-operation")
	parentTraceID := parentSpan.SpanContext().TraceID()

	// Make a gRPC call with the parent trace context
	prepareReq := &snapshotsapi.PrepareSnapshotRequest{
		Key:    "test-snapshot",
		Parent: "",
	}

	_, err := client.Prepare(parentCtx, prepareReq)
	if err != nil {
		t.Fatalf("Failed to prepare snapshot: %v", err)
	}

	parentSpan.End()

	// Verify that the trace ID was propagated
	if len(tracked.capturedTraceIDs) == 0 {
		t.Fatal("No trace IDs were captured in the snapshotter")
	}

	capturedTraceID := tracked.capturedTraceIDs[0]
	if capturedTraceID != parentTraceID {
		t.Errorf("Trace ID was not propagated correctly. Parent: %s, Captured: %s",
			parentTraceID.String(), capturedTraceID.String())
	}

	// Also verify spans were created at different levels
	spans := getTestSpans()

	// We should have spans for:
	// 1. parent-operation (our test span)
	// 2. gRPC client call
	// 3. gRPC server call
	// 4. snapshotter.Prepare (from the tracing wrapper)
	if len(spans) < 3 {
		t.Errorf("Expected at least 3 spans (parent, client, server, snapshotter), got %d", len(spans))
	}

	// Verify all spans in the chain have the same trace ID
	traceIDsSeen := make(map[trace.TraceID]bool)
	for _, span := range spans {
		if span.SpanContext().IsValid() {
			traceIDsSeen[span.SpanContext().TraceID()] = true
		}
	}

	if len(traceIDsSeen) > 1 {
		t.Errorf("Multiple trace IDs found in span chain, trace propagation failed. Found: %v", traceIDsSeen)
	}

	if len(traceIDsSeen) == 1 {
		// Check if it matches our parent trace ID
		for tid := range traceIDsSeen {
			if tid != parentTraceID {
				t.Errorf("Span chain trace ID %s doesn't match parent %s", tid.String(), parentTraceID.String())
			}
		}
	}
}
