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
	"go.opentelemetry.io/otel/baggage"

	"github.com/containerd/accelerated-container-image/pkg/tracing"
)

// baggageTrackingSnapshotter wraps the mock snapshotter to capture baggage values
type baggageTrackingSnapshotter struct {
	*mockSnapshotter
	capturedBaggage []map[string]string
}

func (b *baggageTrackingSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	// Capture all baggage values from context
	baggageMap := tracing.GetBaggageAsMap(ctx)
	b.capturedBaggage = append(b.capturedBaggage, baggageMap)

	return b.mockSnapshotter.Prepare(ctx, key, parent, opts...)
}

func TestBaggagePropagation(t *testing.T) {
	// Initialize test environment
	resetTestSpans()

	// Create baggage tracking snapshotter
	tracked := &baggageTrackingSnapshotter{
		mockSnapshotter: &mockSnapshotter{},
		capturedBaggage: make([]map[string]string, 0),
	}

	// Wrap with service and tracing
	service := snapshotservice.FromSnapshotter(tracked)
	tracedService := tracing.WithTracing(service)

	// Setup gRPC test environment
	ctx, conn, cleanup := newGRPCTestEnv(t, tracedService)
	defer cleanup()

	client := snapshotsapi.NewSnapshotsClient(conn)

	// Create context with baggage values
	testBaggage := map[string]string{
		tracing.BaggageKeyRequestID:   "req-12345",
		tracing.BaggageKeyUserID:      "user-67890",
		tracing.BaggageKeyOperation:   "test-prepare",
		tracing.BaggageKeyImageName:   "test-image",
		tracing.BaggageKeyImageTag:    "latest",
		tracing.BaggageKeyEnvironment: "test",
	}

	// Set baggage in context
	baggageCtx := tracing.SetBaggageFromMap(ctx, testBaggage)

	// Make a gRPC call with baggage
	prepareReq := &snapshotsapi.PrepareSnapshotRequest{
		Key:    "test-snapshot",
		Parent: "",
	}

	_, err := client.Prepare(baggageCtx, prepareReq)
	if err != nil {
		t.Fatalf("Failed to prepare snapshot: %v", err)
	}

	// Verify baggage was propagated to the snapshotter
	if len(tracked.capturedBaggage) == 0 {
		t.Fatal("No baggage was captured in the snapshotter")
	}

	capturedBaggage := tracked.capturedBaggage[0]

	// Verify all expected baggage values were propagated
	for expectedKey, expectedValue := range testBaggage {
		actualValue, ok := capturedBaggage[expectedKey]
		if !ok {
			t.Errorf("Baggage key %s was not propagated", expectedKey)
			continue
		}

		if actualValue != expectedValue {
			t.Errorf("Baggage key %s: expected %s, got %s", expectedKey, expectedValue, actualValue)
		}
	}

	// Verify spans contain baggage attributes
	spans := getTestSpans()
	if len(spans) == 0 {
		t.Fatal("No spans were created")
	}

	// Check that at least one span has baggage attributes
	foundBaggageAttrs := false
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if len(attr.Key) > 8 && string(attr.Key)[:8] == "baggage." {
				foundBaggageAttrs = true

				// Verify the attribute value matches what we set
				baggageKey := string(attr.Key)[8:] // Remove "baggage." prefix
				if expectedValue, ok := testBaggage[baggageKey]; ok {
					if attr.Value.AsString() != expectedValue {
						t.Errorf("Span baggage attribute %s: expected %s, got %s",
							attr.Key, expectedValue, attr.Value.AsString())
					}
				}
			}
		}
	}

	if !foundBaggageAttrs {
		t.Error("No baggage attributes found in spans")
	}
}

func TestBaggageUtilities(t *testing.T) {
	ctx := context.Background()

	// Test individual utility functions
	tests := []struct {
		name     string
		setFunc  func(context.Context) context.Context
		getFunc  func(context.Context) string
		expected string
	}{
		{
			name:     "request_id",
			setFunc:  func(ctx context.Context) context.Context { return tracing.SetRequestID(ctx, "test-req-123") },
			getFunc:  tracing.GetRequestID,
			expected: "test-req-123",
		},
		{
			name:     "user_id",
			setFunc:  func(ctx context.Context) context.Context { return tracing.SetUserID(ctx, "test-user-456") },
			getFunc:  tracing.GetUserID,
			expected: "test-user-456",
		},
		{
			name:     "operation",
			setFunc:  func(ctx context.Context) context.Context { return tracing.SetOperation(ctx, "test-operation") },
			getFunc:  tracing.GetOperation,
			expected: "test-operation",
		},
		{
			name:     "snapshot_key",
			setFunc:  func(ctx context.Context) context.Context { return tracing.SetSnapshotKey(ctx, "test-snap-key") },
			getFunc:  tracing.GetSnapshotKey,
			expected: "test-snap-key",
		},
		{
			name:     "container_id",
			setFunc:  func(ctx context.Context) context.Context { return tracing.SetContainerID(ctx, "test-container-789") },
			getFunc:  tracing.GetContainerID,
			expected: "test-container-789",
		},
		{
			name:     "namespace",
			setFunc:  func(ctx context.Context) context.Context { return tracing.SetNamespace(ctx, "test-namespace") },
			getFunc:  tracing.GetNamespace,
			expected: "test-namespace",
		},
		{
			name:     "environment",
			setFunc:  func(ctx context.Context) context.Context { return tracing.SetEnvironment(ctx, "test-env") },
			getFunc:  tracing.GetEnvironment,
			expected: "test-env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set the value
			ctxWithValue := tt.setFunc(ctx)

			// Get the value back
			actual := tt.getFunc(ctxWithValue)

			if actual != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, actual)
			}

			// Verify it's also in the raw baggage
			bag := baggage.FromContext(ctxWithValue)
			members := bag.Members()

			found := false
			for _, member := range members {
				if member.Value() == tt.expected {
					found = true
					break
				}
			}

			if !found {
				t.Errorf("Value %s not found in raw baggage", tt.expected)
			}
		})
	}
}

func TestImageInfo(t *testing.T) {
	ctx := context.Background()

	expectedName := "test-image"
	expectedTag := "v1.0.0"

	// Set image info
	ctxWithImage := tracing.SetImageInfo(ctx, expectedName, expectedTag)

	// Get image info back
	actualName, actualTag := tracing.GetImageInfo(ctxWithImage)

	if actualName != expectedName {
		t.Errorf("Image name: expected %s, got %s", expectedName, actualName)
	}

	if actualTag != expectedTag {
		t.Errorf("Image tag: expected %s, got %s", expectedTag, actualTag)
	}
}

func TestBaggageFromMap(t *testing.T) {
	ctx := context.Background()

	testData := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
		"":     "empty-key", // Should be skipped
		"key4": "",          // Should be skipped
	}

	// Set baggage from map
	ctxWithBaggage := tracing.SetBaggageFromMap(ctx, testData)

	// Get baggage back as map
	actualMap := tracing.GetBaggageAsMap(ctxWithBaggage)

	// Verify expected values are present
	expectedKeys := []string{"key1", "key2", "key3"}
	for _, key := range expectedKeys {
		if actualValue, ok := actualMap[key]; !ok {
			t.Errorf("Expected key %s not found", key)
		} else if actualValue != testData[key] {
			t.Errorf("Key %s: expected %s, got %s", key, testData[key], actualValue)
		}
	}

	// Verify empty key and empty value are not present
	if _, ok := actualMap[""]; ok {
		t.Error("Empty key should not be present")
	}

	if actualValue, ok := actualMap["key4"]; ok && actualValue == "" {
		t.Error("Key with empty value should not be present")
	}
}
