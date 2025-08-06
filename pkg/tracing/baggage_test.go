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

	"go.opentelemetry.io/otel/baggage"

	"github.com/containerd/accelerated-container-image/pkg/tracing"
)

func TestSimplifiedBaggage(t *testing.T) {
	ctx := context.Background()

	// Test SetRequestID and GetRequestID
	ctx = tracing.SetRequestID(ctx, "test-12345")
	requestID := tracing.GetRequestID(ctx)

	if requestID != "test-12345" {
		t.Errorf("Expected request ID 'test-12345', got '%s'", requestID)
	}

	// Test direct baggage access
	bag := baggage.FromContext(ctx)
	member := bag.Member("request.id")
	if member.Value() != "test-12345" {
		t.Errorf("Expected baggage value 'test-12345', got '%s'", member.Value())
	}
}