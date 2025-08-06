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
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var (
	testProcessor *mockSpanProcessor
	origProvider  = otel.GetTracerProvider()
)

func TestMain(m *testing.M) {
	// Setup test environment
	testProcessor = &mockSpanProcessor{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(testProcessor),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// Run tests
	code := m.Run()

	// Cleanup
	_ = tp.Shutdown(context.Background())
	otel.SetTracerProvider(origProvider)

	os.Exit(code)
}

type mockSpanProcessor struct {
	spans []sdktrace.ReadOnlySpan
}

func (p *mockSpanProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {}

func (p *mockSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	p.spans = append(p.spans, s)
}

func (p *mockSpanProcessor) Shutdown(context.Context) error { return nil }

func (p *mockSpanProcessor) ForceFlush(context.Context) error { return nil }

func (p *mockSpanProcessor) Reset() {
	p.spans = nil
}

// getTestSpans returns all spans collected since the last Reset
func getTestSpans() []sdktrace.ReadOnlySpan {
	return testProcessor.spans
}

// resetTestSpans clears all collected spans
func resetTestSpans() {
	testProcessor.Reset()
}
