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

package convertor

import (
	"errors"
	"os"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestCalculateTotalSize(t *testing.T) {
	descs := []ocispec.Descriptor{
		{Size: 100},
		{Size: 200},
		{Size: 300},
	}
	if size := calculateTotalSize(descs); size != 600 {
		t.Errorf("Expected total size 600, got %d", size)
	}
}

func TestGetCompressionType(t *testing.T) {
	tests := []struct {
		name     string
		desc     ocispec.Descriptor
		expected string
	}{
		{
			name:     "gzip",
			desc:     ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip},
			expected: "gzip",
		},
		{
			name:     "zstd",
			desc:     ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerZstd},
			expected: "zstd",
		},
		{
			name:     "none",
			desc:     ocispec.Descriptor{MediaType: "unknown"},
			expected: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if comp := getCompressionType(tt.desc); comp != tt.expected {
				t.Errorf("Expected compression %s, got %s", tt.expected, comp)
			}
		})
	}
}

func TestCalculateThroughput(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		duration time.Duration
		expected int64
	}{
		{
			name:     "normal case",
			bytes:    1000,
			duration: time.Second,
			expected: 1000,
		},
		{
			name:     "zero duration",
			bytes:    1000,
			duration: 0,
			expected: 0,
		},
		{
			name:     "zero bytes",
			bytes:    0,
			duration: time.Second,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if throughput := calculateThroughput(tt.bytes, tt.duration); throughput != tt.expected {
				t.Errorf("Expected throughput %d, got %d", tt.expected, throughput)
			}
		})
	}
}

func TestGetErrorType(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "simple error",
			err:      errors.New("test error"),
			expected: "test error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errType := getErrorType(tt.err); errType != tt.expected {
				t.Errorf("Expected error type %q, got %q", tt.expected, errType)
			}
		})
	}
}

func TestGetDiskUsage(t *testing.T) {
	// Create a temporary directory
	dir, err := os.MkdirTemp("", "test-disk-usage")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write some data
	if err := os.WriteFile(dir+"/testfile", []byte("test data"), 0644); err != nil {
		t.Fatal(err)
	}

	usage := getDiskUsage(dir)
	if usage <= 0 {
		t.Errorf("Expected positive disk usage, got %d", usage)
	}
}