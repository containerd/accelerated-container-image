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
	"net"
	"testing"
	"time"

	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	"github.com/containerd/containerd/v2/contrib/snapshotservice"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/containerd/accelerated-container-image/pkg/tracing"
)

const bufSize = 1024 * 1024

type mockSnapshotter struct {
	snapshots.Snapshotter
}

func (m *mockSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	return []mount.Mount{{Type: "mock"}}, nil
}

func (m *mockSnapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	return nil
}

func (m *mockSnapshotter) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	return []mount.Mount{{Type: "mock"}}, nil
}

func (m *mockSnapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	return []mount.Mount{{Type: "mock"}}, nil
}

func (m *mockSnapshotter) List(ctx context.Context, filters ...string) ([]snapshots.Info, error) {
	return []snapshots.Info{{Name: "test-commit"}}, nil
}

func (m *mockSnapshotter) Remove(ctx context.Context, key string) error {
	return nil
}

func (m *mockSnapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, filters ...string) error {
	info := snapshots.Info{Name: "test-commit"}
	return fn(ctx, info)
}

func (m *mockSnapshotter) Close() error {
	return nil
}

func newGRPCTestEnv(t *testing.T, snapshotter snapshotsapi.SnapshotsServer) (context.Context, *grpc.ClientConn, func()) {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(tracing.WithServerTracing())
	snapshotsapi.RegisterSnapshotsServer(srv, snapshotter)

	go func() {
		if err := srv.Serve(lis); err != nil {
			// Only log if the error is not due to server being stopped
			if err != grpc.ErrServerStopped {
				t.Logf("Server error: %v", err)
			}
		}
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithInsecure(),
		tracing.WithClientTracing(),
	)
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}

	cleanup := func() {
		conn.Close()
		srv.Stop()
		lis.Close()
	}

	return ctx, conn, cleanup
}

func TestTracingSnapshotterIntegration(t *testing.T) {
	// Create and wrap the snapshotter
	snapshotter := &mockSnapshotter{}
	service := snapshotservice.FromSnapshotter(snapshotter)
	tracedService := tracing.WithTracing(service)

	// Setup gRPC server and client
	ctx, conn, cleanup := newGRPCTestEnv(t, tracedService)
	defer cleanup()

	// Create client
	client := snapshotsapi.NewSnapshotsClient(conn)

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "prepare and commit",
			run: func(t *testing.T) {
				resetTestSpans()

				prepareReq := &snapshotsapi.PrepareSnapshotRequest{
					Key:    "test-snapshot",
					Parent: "",
				}
				prepareResp, err := client.Prepare(ctx, prepareReq)
				if err != nil {
					t.Fatalf("Failed to prepare snapshot: %v", err)
				}

				if len(prepareResp.Mounts) == 0 {
					t.Error("No mounts returned from prepare")
				}

				commitReq := &snapshotsapi.CommitSnapshotRequest{
					Name: "test-commit",
					Key:  "test-snapshot",
				}
				_, err = client.Commit(ctx, commitReq)
				if err != nil {
					t.Fatalf("Failed to commit snapshot: %v", err)
				}

				// Verify spans (otelgrpc creates more spans than our old custom interceptor)
				spans := getTestSpans()
				if len(spans) < 2 {
					t.Errorf("got %d spans, want at least 2", len(spans))
				}
			},
		},
		{
			name: "list snapshots",
			run: func(t *testing.T) {
				resetTestSpans()

				listReq := &snapshotsapi.ListSnapshotsRequest{}
				listClient, err := client.List(ctx, listReq)
				if err != nil {
					t.Fatalf("Failed to list snapshots: %v", err)
				}

				var snapshots []*snapshotsapi.Info
				for {
					resp, err := listClient.Recv()
					if err != nil {
						break
					}
					snapshots = append(snapshots, resp.Info...)
				}

				found := false
				for _, info := range snapshots {
					if info.Name == "test-commit" {
						found = true
						break
					}
				}
				if !found {
					t.Error("Committed snapshot not found in list")
				}

				// Verify spans (otelgrpc creates more spans than our old custom interceptor)
				spans := getTestSpans()
				if len(spans) < 1 {
					t.Errorf("got %d spans, want at least 1", len(spans))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t)
			// Give time for spans to be processed
			time.Sleep(10 * time.Millisecond)
		})
	}
}
