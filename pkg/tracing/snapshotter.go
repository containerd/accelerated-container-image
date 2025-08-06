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

	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	ptypes "github.com/containerd/containerd/v2/pkg/protobuf/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "accelerated-container-image/snapshotter"

// TracingSnapshotter wraps a snapshotter service with OpenTelemetry tracing
type TracingSnapshotter struct {
	server snapshotsapi.SnapshotsServer
	snapshotsapi.UnimplementedSnapshotsServer
}

// Server returns the underlying snapshotter server for testing
func (s *TracingSnapshotter) Server() snapshotsapi.SnapshotsServer {
	return s.server
}

// WithTracing wraps a snapshotter service with OpenTelemetry tracing
func WithTracing(server snapshotsapi.SnapshotsServer) snapshotsapi.SnapshotsServer {
	return &TracingSnapshotter{server: server}
}

func (s *TracingSnapshotter) Prepare(ctx context.Context, pr *snapshotsapi.PrepareSnapshotRequest) (*snapshotsapi.PrepareSnapshotResponse, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Prepare", trace.WithAttributes(
		attribute.String("key", pr.Key),
		attribute.String("parent", pr.Parent),
	))
	defer span.End()

	resp, err := s.server.Prepare(ctx, pr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}

func (s *TracingSnapshotter) View(ctx context.Context, pr *snapshotsapi.ViewSnapshotRequest) (*snapshotsapi.ViewSnapshotResponse, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.View", trace.WithAttributes(
		attribute.String("key", pr.Key),
		attribute.String("parent", pr.Parent),
	))
	defer span.End()

	resp, err := s.server.View(ctx, pr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}

func (s *TracingSnapshotter) Mounts(ctx context.Context, mr *snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Mounts", trace.WithAttributes(
		attribute.String("key", mr.Key),
	))
	defer span.End()

	resp, err := s.server.Mounts(ctx, mr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}

func (s *TracingSnapshotter) Commit(ctx context.Context, cr *snapshotsapi.CommitSnapshotRequest) (*ptypes.Empty, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Commit", trace.WithAttributes(
		attribute.String("name", cr.Name),
		attribute.String("key", cr.Key),
	))
	defer span.End()

	resp, err := s.server.Commit(ctx, cr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}

func (s *TracingSnapshotter) Remove(ctx context.Context, rr *snapshotsapi.RemoveSnapshotRequest) (*ptypes.Empty, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Remove", trace.WithAttributes(
		attribute.String("key", rr.Key),
	))
	defer span.End()

	resp, err := s.server.Remove(ctx, rr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}

func (s *TracingSnapshotter) Stat(ctx context.Context, sr *snapshotsapi.StatSnapshotRequest) (*snapshotsapi.StatSnapshotResponse, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Stat", trace.WithAttributes(
		attribute.String("key", sr.Key),
	))
	defer span.End()

	resp, err := s.server.Stat(ctx, sr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}

func (s *TracingSnapshotter) Update(ctx context.Context, sr *snapshotsapi.UpdateSnapshotRequest) (*snapshotsapi.UpdateSnapshotResponse, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Update", trace.WithAttributes(
		attribute.String("name", sr.Info.Name),
	))
	defer span.End()

	resp, err := s.server.Update(ctx, sr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}

func (s *TracingSnapshotter) List(sr *snapshotsapi.ListSnapshotsRequest, ss snapshotsapi.Snapshots_ListServer) error {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ss.Context(), "snapshotter.List")
	defer span.End()

	err := s.server.List(sr, &tracingListServer{
		Snapshots_ListServer: ss,
		ctx:                 ctx,
	})
	if err != nil {
		span.RecordError(err)
	}
	return err
}

type tracingListServer struct {
	snapshotsapi.Snapshots_ListServer
	ctx context.Context
}

func (t *tracingListServer) Context() context.Context {
	return t.ctx
}

func (s *TracingSnapshotter) Usage(ctx context.Context, ur *snapshotsapi.UsageRequest) (*snapshotsapi.UsageResponse, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Usage", trace.WithAttributes(
		attribute.String("key", ur.Key),
	))
	defer span.End()

	resp, err := s.server.Usage(ctx, ur)
	if err != nil {
		span.RecordError(err)
	}
	if resp != nil {
		span.SetAttributes(
			attribute.Int64("inodes", resp.Inodes),
			attribute.Int64("size", resp.Size),
		)
	}
	return resp, err
}

func (s *TracingSnapshotter) Cleanup(ctx context.Context, cr *snapshotsapi.CleanupRequest) (*ptypes.Empty, error) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, "snapshotter.Cleanup")
	defer span.End()

	resp, err := s.server.Cleanup(ctx, cr)
	if err != nil {
		span.RecordError(err)
	}
	return resp, err
}