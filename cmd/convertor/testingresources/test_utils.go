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

package testingresources

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/pkg/testutil"
)

func GetLocalRegistryPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return path.Join(cwd, "..", "testingresources", "mocks", "registry")
}

// GetTestRegistry returns a TestRegistry with the specified options. If opts.LocalRegistryPath is not specified,
// the default local registry path will be used.
func GetTestRegistry(t *testing.T, ctx context.Context, opts RegistryOptions) *TestRegistry {
	if opts.LocalRegistryPath == "" {
		opts.LocalRegistryPath = GetLocalRegistryPath()
	}
	reg, err := NewTestRegistry(ctx, opts)
	if err != nil {
		t.Error(err)
	}
	return reg
}

func GetTestResolver(t *testing.T, ctx context.Context) remotes.Resolver {
	localRegistryPath := GetLocalRegistryPath()
	resolver, err := NewMockLocalResolver(ctx, localRegistryPath)
	if err != nil {
		t.Error(err)
	}
	return resolver
}

func GetCustomTestResolver(t *testing.T, ctx context.Context, testRegistry *TestRegistry) remotes.Resolver {
	resolver, err := NewCustomMockLocalResolver(ctx, testRegistry)
	if err != nil {
		t.Error(err)
	}
	return resolver
}

func GetTestFetcherFromResolver(t *testing.T, ctx context.Context, resolver remotes.Resolver, ref string) remotes.Fetcher {
	fetcher, err := resolver.Fetcher(ctx, ref)
	if err != nil {
		t.Error(err)
	}
	return fetcher
}

func GetTestPusherFromResolver(t *testing.T, ctx context.Context, resolver remotes.Resolver, ref string) remotes.Pusher {
	pusher, err := resolver.Pusher(ctx, ref)
	if err != nil {
		t.Error(err)
	}
	return pusher
}

func Assert(t *testing.T, condition bool, msg string) {
	if !condition {
		t.Error(msg)
	}
}

// RunTestWithTempDir runs the specified test function with a temporary writable directory.
func RunTestWithTempDir(t *testing.T, ctx context.Context, name string, testFn func(t *testing.T, ctx context.Context, tmpDir string)) {
	tmpDir := t.TempDir()

	work := filepath.Join(tmpDir, "work")
	if err := os.MkdirAll(work, 0777); err != nil {
		t.Fatal(err)
	}

	defer testutil.DumpDirOnFailure(t, tmpDir)
	t.Run(name, func(t *testing.T) {
		testFn(t, ctx, work)
	})
}
