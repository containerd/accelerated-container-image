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
	"errors"
	"testing"

	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	"github.com/containerd/containerd/v2/core/mount"
	"go.opentelemetry.io/otel"

	"github.com/containerd/accelerated-container-image/pkg/tracing"
)

type mockSnapshotsServer struct {
	snapshotsapi.UnimplementedSnapshotsServer
	prepareCalled bool
	prepareError  error
	viewCalled    bool
	viewError     error
	mountsCalled  bool
	mountsError   error
}

func (m *mockSnapshotsServer) Prepare(ctx context.Context, req *snapshotsapi.PrepareSnapshotRequest) (*snapshotsapi.PrepareSnapshotResponse, error) {
	m.prepareCalled = true
	if m.prepareError != nil {
		return nil, m.prepareError
	}
	return &snapshotsapi.PrepareSnapshotResponse{
		Mounts: mount.ToProto([]mount.Mount{{Type: "test"}}),
	}, nil
}

func (m *mockSnapshotsServer) View(ctx context.Context, req *snapshotsapi.ViewSnapshotRequest) (*snapshotsapi.ViewSnapshotResponse, error) {
	m.viewCalled = true
	if m.viewError != nil {
		return nil, m.viewError
	}
	return &snapshotsapi.ViewSnapshotResponse{
		Mounts: mount.ToProto([]mount.Mount{{Type: "test"}}),
	}, nil
}

func (m *mockSnapshotsServer) Mounts(ctx context.Context, req *snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
	m.mountsCalled = true
	if m.mountsError != nil {
		return nil, m.mountsError
	}
	return &snapshotsapi.MountsResponse{
		Mounts: mount.ToProto([]mount.Mount{{Type: "test"}}),
	}, nil
}

func TestTracingSnapshotter_Operations(t *testing.T) {
	tests := []struct {
		name        string
		operation   string
		runTest     func(context.Context, snapshotsapi.SnapshotsServer) error
		withError   bool
		attributes  map[string]string
	}{
		{
			name:      "prepare success",
			operation: "snapshotter.Prepare",
			runTest: func(ctx context.Context, s snapshotsapi.SnapshotsServer) error {
				_, err := s.Prepare(ctx, &snapshotsapi.PrepareSnapshotRequest{
					Key:    "test-key",
					Parent: "test-parent",
				})
				return err
			},
			attributes: map[string]string{
				"key":    "test-key",
				"parent": "test-parent",
			},
		},
		{
			name:      "prepare error",
			operation: "snapshotter.Prepare",
			runTest: func(ctx context.Context, s snapshotsapi.SnapshotsServer) error {
				mock := s.(*tracing.TracingSnapshotter).Server().(*mockSnapshotsServer)
				mock.prepareError = errors.New("prepare failed")
				_, err := s.Prepare(ctx, &snapshotsapi.PrepareSnapshotRequest{
					Key:    "test-key",
					Parent: "test-parent",
				})
				return err
			},
			withError: true,
			attributes: map[string]string{
				"key":    "test-key",
				"parent": "test-parent",
			},
		},
		{
			name:      "view success",
			operation: "snapshotter.View",
			runTest: func(ctx context.Context, s snapshotsapi.SnapshotsServer) error {
				_, err := s.View(ctx, &snapshotsapi.ViewSnapshotRequest{
					Key:    "test-key",
					Parent: "test-parent",
				})
				return err
			},
			attributes: map[string]string{
				"key":    "test-key",
				"parent": "test-parent",
			},
		},
		{
			name:      "mounts success",
			operation: "snapshotter.Mounts",
			runTest: func(ctx context.Context, s snapshotsapi.SnapshotsServer) error {
				_, err := s.Mounts(ctx, &snapshotsapi.MountsRequest{
					Key: "test-key",
				})
				return err
			},
			attributes: map[string]string{
				"key": "test-key",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTestSpans()
			mock := &mockSnapshotsServer{}
			sut := tracing.WithTracing(mock)

			err := tt.runTest(context.Background(), sut)

			if (err != nil) != tt.withError {
				t.Errorf("got error = %v, wantError = %v", err, tt.withError)
			}

			spans := getTestSpans()
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}

			span := spans[0]
			if span.Name() != tt.operation {
				t.Errorf("got span name = %s, want %s", span.Name(), tt.operation)
			}

			attrs := make(map[string]string)
			for _, attr := range span.Attributes() {
				attrs[string(attr.Key)] = attr.Value.AsString()
			}

			for k, v := range tt.attributes {
				if got := attrs[k]; got != v {
					t.Errorf("attribute %s = %s, want %s", k, got, v)
				}
			}

			if tt.withError && len(span.Events()) != 1 {
				t.Error("error event not recorded in span")
			}
		})
	}
}

func TestTracingContextPropagation(t *testing.T) {
	resetTestSpans()
	mock := &mockSnapshotsServer{}
	sut := tracing.WithTracing(mock)

	tracer := otel.GetTracerProvider().Tracer("test")
	ctx, parentSpan := tracer.Start(context.Background(), "parent")
	defer parentSpan.End()

	_, err := sut.Prepare(ctx, &snapshotsapi.PrepareSnapshotRequest{
		Key:    "test-key",
		Parent: "test-parent",
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	spans := getTestSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}

	span := spans[0]
	if span.Parent().TraceID() != parentSpan.SpanContext().TraceID() {
		t.Error("trace context not properly propagated")
	}
}