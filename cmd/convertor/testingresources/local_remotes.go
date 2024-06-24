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
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	labelDistributionSource = "containerd.io/distribution.source"
)

// RESOLVER
type MockLocalResolver struct {
	testReg *TestRegistry
}

func NewMockLocalResolver(ctx context.Context, localRegistryPath string) (*MockLocalResolver, error) {
	reg, err := NewTestRegistry(ctx, RegistryOptions{
		LocalRegistryPath:         localRegistryPath,
		InmemoryRegistryOnly:      false,
		ManifestPushIgnoresLayers: false,
	})
	if err != nil {
		return nil, err
	}

	return &MockLocalResolver{
		testReg: reg,
	}, nil
}

func NewCustomMockLocalResolver(ctx context.Context, testReg *TestRegistry) (*MockLocalResolver, error) {
	return &MockLocalResolver{
		testReg: testReg,
	}, nil
}

func (r *MockLocalResolver) Resolve(ctx context.Context, ref string) (string, v1.Descriptor, error) {
	desc, err := r.testReg.Resolve(ctx, ref)
	if err != nil {
		return "", v1.Descriptor{}, err
	}
	return "", desc, nil
}

func (r *MockLocalResolver) Fetcher(ctx context.Context, ref string) (remotes.Fetcher, error) {
	_, repository, _, err := ParseRef(ctx, ref)
	if err != nil {
		return nil, err
	}

	return &MockLocalFetcher{
		testReg:    r.testReg,
		repository: repository,
	}, nil
}

func (r *MockLocalResolver) Pusher(ctx context.Context, ref string) (remotes.Pusher, error) {
	host, repository, tag, err := ParseRef(ctx, ref)
	if err != nil {
		return nil, err
	}

	return &MockLocalPusher{
		testReg:    r.testReg,
		repository: repository,
		tag:        tag,
		host:       host,
		tracker:    docker.NewInMemoryTracker(),
	}, nil
}

// FETCHER
type MockLocalFetcher struct {
	testReg    *TestRegistry
	repository string
}

func (f *MockLocalFetcher) Fetch(ctx context.Context, desc v1.Descriptor) (io.ReadCloser, error) {
	return f.testReg.Fetch(ctx, f.repository, desc)
}

// PUSHER
type MockLocalPusher struct {
	testReg    *TestRegistry
	repository string
	tag        string
	host       string
	tracker    docker.StatusTracker
}

// Not used by overlaybd conversion
func (p MockLocalPusher) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return nil, errors.New("Not implemented")
}

func (p MockLocalPusher) Push(ctx context.Context, desc v1.Descriptor) (content.Writer, error) {
	return p.push(ctx, desc, remotes.MakeRefKey(ctx, desc), false)
}

func (p MockLocalPusher) push(ctx context.Context, desc v1.Descriptor, ref string, unavailableOnFail bool) (content.Writer, error) {
	if l, ok := p.tracker.(docker.StatusTrackLocker); ok {
		l.Lock(ref)
		defer l.Unlock(ref)
	}

	status, err := p.tracker.GetStatus(ref)
	if err == nil {
		if status.Committed && status.Offset == status.Total {
			return nil, fmt.Errorf("ref %v: %w", ref, errdefs.ErrAlreadyExists)
		}
		if unavailableOnFail && status.ErrClosed == nil {
			// Another push of this ref is happening elsewhere. The rest of function
			// will continue only when `errdefs.IsNotFound(err) == true` (i.e. there
			// is no actively-tracked ref already).
			return nil, fmt.Errorf("push is on-going: %w", errdefs.ErrUnavailable)
		}
	} else if !errdefs.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}

	// Check Exists first
	ok, err := p.testReg.Exists(ctx, p.repository, p.tag, desc)

	if err != nil {
		return nil, err
	}

	if ok {
		return nil, errdefs.ErrAlreadyExists
	}

	// Layer mounts
	if mountRepo, ok := desc.Annotations[fmt.Sprintf("%s.%s", labelDistributionSource, p.host)]; ok {
		err = p.testReg.Mount(ctx, mountRepo, p.repository, desc)
		if err != nil {
			return nil, err
		}
		return nil, errdefs.ErrAlreadyExists
	}

	respC := make(chan error, 1)

	p.tracker.SetStatus(ref, docker.Status{
		Status: content.Status{
			Ref:       ref,
			Total:     desc.Size,
			Expected:  desc.Digest,
			StartedAt: time.Now(),
		},
	})

	pr, pw := io.Pipe()
	body := io.NopCloser(pr)

	go func() {
		defer close(respC)

		// Reader must be read first before other error checks
		buf, err := io.ReadAll(body)
		if err != nil {
			respC <- err
			pr.CloseWithError(err)
			return
		}

		err = p.testReg.Push(ctx, p.repository, p.tag, desc, buf)

		if err != nil {
			respC <- err
			pr.CloseWithError(err)
			return
		}
		respC <- nil
	}()

	return &pushWriter{
		ref:       ref,
		pipe:      pw,
		responseC: respC,
		expected:  desc.Digest,
		tracker:   p.tracker,
	}, nil
}

type pushWriter struct {
	ref       string
	pipe      *io.PipeWriter
	responseC <-chan error

	expected digest.Digest
	tracker  docker.StatusTracker
}

func (pw *pushWriter) Write(p []byte) (n int, err error) {
	status, err := pw.tracker.GetStatus(pw.ref)
	if err != nil {
		return n, err
	}
	n, err = pw.pipe.Write(p)
	status.Offset += int64(n)
	status.UpdatedAt = time.Now()
	pw.tracker.SetStatus(pw.ref, status)
	return
}

func (pw *pushWriter) Close() error {
	status, err := pw.tracker.GetStatus(pw.ref)
	if err == nil && !status.Committed {
		// Closing an incomplete writer. Record this as an error so that following write can retry it.
		status.ErrClosed = errors.New("closed incomplete writer")
		pw.tracker.SetStatus(pw.ref, status)
	}
	return pw.pipe.Close()
}

func (pw *pushWriter) Status() (content.Status, error) {
	status, err := pw.tracker.GetStatus(pw.ref)
	if err != nil {
		return content.Status{}, err
	}
	return status.Status, nil
}

func (pw *pushWriter) Digest() digest.Digest {
	return pw.expected
}

func (pw *pushWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	// Check whether read has already thrown an error
	if _, err := pw.pipe.Write([]byte{}); err != nil && err != io.ErrClosedPipe {
		return fmt.Errorf("pipe error before commit: %w", err)
	}

	if err := pw.pipe.Close(); err != nil {
		return err
	}

	err := <-pw.responseC
	if err != nil {
		return err
	}

	status, err := pw.tracker.GetStatus(pw.ref)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	if size > 0 && size != status.Offset {
		return fmt.Errorf("unexpected size %d, expected %d", status.Offset, size)
	}
	status.Committed = true
	status.UpdatedAt = time.Now()
	pw.tracker.SetStatus(pw.ref, status)
	return nil
}

func (pw *pushWriter) Truncate(size int64) error {
	return errors.New("cannot truncate remote upload")
}
