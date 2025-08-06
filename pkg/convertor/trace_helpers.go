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
	"runtime"
	"syscall"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// calculateTotalSize returns the total size of all descriptors
func calculateTotalSize(descs []ocispec.Descriptor) int64 {
	var total int64
	for _, desc := range descs {
		total += desc.Size
	}
	return total
}

// getCompressionType determines the compression type from media type
func getCompressionType(desc ocispec.Descriptor) string {
	switch desc.MediaType {
	case ocispec.MediaTypeImageLayerGzip:
		return "gzip"
	case ocispec.MediaTypeImageLayerZstd:
		return "zstd"
	default:
		return "none"
	}
}

// getMemoryUsage returns the current memory usage in bytes
func getMemoryUsage() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.Alloc)
}

// getDiskUsage returns the current disk usage for the given path
func getDiskUsage(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	return int64(stat.Blocks-stat.Bfree) * int64(stat.Bsize)
}

// calculateThroughput calculates bytes per second
func calculateThroughput(bytesProcessed int64, duration time.Duration) int64 {
	if duration.Seconds() == 0 {
		return 0
	}
	return int64(float64(bytesProcessed) / duration.Seconds())
}

// getErrorType returns a string representation of the error type
func getErrorType(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// addLayerAttributes adds common layer attributes to a span
func addLayerAttributes(span trace.Span, desc ocispec.Descriptor, idx int) {
	span.SetAttributes(
		attribute.Int("layer_index", idx),
		attribute.String("digest", desc.Digest.String()),
		attribute.Int64("size", desc.Size),
		attribute.String("media_type", desc.MediaType),
		attribute.String("compression", getCompressionType(desc)),
	)
}

// addResourceAttributes adds resource usage attributes to a span
func addResourceAttributes(span trace.Span, path string) {
	span.SetAttributes(
		attribute.Int64("memory_used_bytes", getMemoryUsage()),
		attribute.Int64("disk_used_bytes", getDiskUsage(path)),
	)
}

// addConfigAttributes adds configuration attributes to a span
func addConfigAttributes(span trace.Span, cfg ZFileConfig, vsize int) {
	span.SetAttributes(
		attribute.String("algorithm", cfg.Algorithm),
		attribute.Int("block_size", cfg.BlockSize),
		attribute.Int("vsize", vsize),
	)
}

// addProgressEvent adds a progress event to a span
func addProgressEvent(span trace.Span, processed, total int64, blocks int) {
	var percent float64
	if total > 0 {
		percent = float64(processed) / float64(total) * 100
	}
	span.AddEvent("conversion_progress", trace.WithAttributes(
		attribute.Int64("bytes_processed", processed),
		attribute.Float64("percent_complete", percent),
		attribute.Int("blocks_converted", blocks),
	))
}

// addErrorEvent adds detailed error information to a span
func addErrorEvent(span trace.Span, err error, operation string, desc ocispec.Descriptor) {
	if err == nil {
		return
	}
	span.RecordError(err, trace.WithAttributes(
		attribute.String("operation_type", operation),
		attribute.String("layer_digest", desc.Digest.String()),
		attribute.String("error_type", getErrorType(err)),
	))
}