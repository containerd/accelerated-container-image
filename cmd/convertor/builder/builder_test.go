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

package builder

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	_ "github.com/containerd/containerd/v2/pkg/testutil" // Handle custom root flag
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

// Test_builder_Err_Fuzz_Build This test is for the arguably complex error handling and potential go routine
// locking that can happen for the builder component. It works by testing multiple potential error patterns
// across all stages of the process (Through consistent pseudo random generation, for reproducibility and
// ease of adjustment). The test is designed to run in parallel to maximize the chance of a contention.
func Test_builder_Build_Contention(t *testing.T) {
	// If timeout of 1 second is exceeded with the mock fuz builder engine
	// then there is a high likelihood of a present contention error
	contentionTimeout := time.Second * 1
	patternCount := int64(500) // Try out 500 different seeds

	var i int64
	for i = 0; i < patternCount; i++ {
		seed := i
		t.Run(fmt.Sprintf("Test_builder_Err_Lock Contention Seed %d", i), func(t *testing.T) {
			t.Parallel()
			fixedRand := rand.New(rand.NewSource(seed))
			builderEngine := newMockBuilderEngine(fixedRand)
			b := &overlaybdBuilder{
				engine: builderEngine,
				layers: 25,
			}
			ctx, cancel := context.WithTimeout(context.Background(), contentionTimeout)
			defer cancel()

			b.Build(ctx)
			// Build will typically return an error but completes successfully for some seeds as well
			if ctx.Err() != nil {
				if ctx.Err() == context.DeadlineExceeded {
					t.Errorf("Context deadline was exceeded, likely contention error")
				}
			}
		})
	}
}

const (
	failRate = 0.05 // 5% of the time, fail any given operation
)

type mockFuzzBuilderEngine struct {
	fixedRand *rand.Rand
}

func newMockBuilderEngine(fixedRand *rand.Rand) builderEngine {
	return &mockFuzzBuilderEngine{
		fixedRand: fixedRand,
	}
}

func (e *mockFuzzBuilderEngine) DownloadLayer(ctx context.Context, idx int) error {
	if e.fixedRand.Float64() < failRate {
		return fmt.Errorf("random error on download")
	}
	return nil
}

func (e *mockFuzzBuilderEngine) BuildLayer(ctx context.Context, idx int) error {
	if e.fixedRand.Float64() < failRate {
		return fmt.Errorf("random error on BuildLayer")
	}
	return nil
}

func (e *mockFuzzBuilderEngine) UploadLayer(ctx context.Context, idx int) error {
	if e.fixedRand.Float64() < failRate {
		return fmt.Errorf("random error on UploadLayer")
	}
	return nil
}

func (e *mockFuzzBuilderEngine) UploadImage(ctx context.Context) (specs.Descriptor, error) {
	if e.fixedRand.Float64() < failRate {
		return specs.Descriptor{}, fmt.Errorf("random error on UploadImage")
	}
	return specs.Descriptor{}, nil
}

func (e *mockFuzzBuilderEngine) CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error) {
	if e.fixedRand.Float64() < failRate {
		return specs.Descriptor{}, fmt.Errorf("random error on CheckForConvertedLayer")
	}
	return specs.Descriptor{}, nil
}

func (e *mockFuzzBuilderEngine) StoreConvertedLayerDetails(ctx context.Context, idx int) error {
	if e.fixedRand.Float64() < failRate {
		return fmt.Errorf("random error on StoreConvertedLayerDetails")
	}
	return nil
}

func (e *mockFuzzBuilderEngine) CheckForConvertedManifest(ctx context.Context) (specs.Descriptor, error) {
	if e.fixedRand.Float64() < failRate {
		return specs.Descriptor{}, fmt.Errorf("random error on CheckForConvertedManifest")
	}
	return specs.Descriptor{}, nil
}

func (e *mockFuzzBuilderEngine) StoreConvertedManifestDetails(ctx context.Context) error {
	if e.fixedRand.Float64() < failRate {
		return fmt.Errorf("random error on StoreConvertedManifestDetails")
	}
	return nil
}

func (e *mockFuzzBuilderEngine) DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error {
	if e.fixedRand.Float64() < failRate {
		return fmt.Errorf("random error on DownloadConvertedLayer")
	}
	return nil
}

func (e *mockFuzzBuilderEngine) TagPreviouslyConvertedManifest(ctx context.Context, desc specs.Descriptor) error {
	return nil
}

func (e *mockFuzzBuilderEngine) Cleanup() {
}
