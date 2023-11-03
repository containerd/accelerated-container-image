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

package utils

import (
	"context"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/containerd/containerd/log"
	"github.com/pkg/errors"
)

const (
	obdBinCreate = "/opt/overlaybd/bin/overlaybd-create"
	obdBinCommit = "/opt/overlaybd/bin/overlaybd-commit"
	obdBinApply  = "/opt/overlaybd/bin/overlaybd-apply"

	dataFile       = "writable_data"
	idxFile        = "writable_index"
	sealedFile     = "overlaybd.sealed"
	commitTempFile = "overlaybd.commit.temp"
	commitFile     = "overlaybd.commit"
)

func Create(ctx context.Context, dir string, opts ...string) error {
	dataPath := path.Join(dir, dataFile)
	indexPath := path.Join(dir, idxFile)
	os.RemoveAll(dataPath)
	os.RemoveAll(indexPath)
	args := append([]string{dataPath, indexPath}, opts...)
	log.G(ctx).Debugf("%s %s", obdBinCreate, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, obdBinCreate, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-create: %s", out)
	}
	return nil
}

func Seal(ctx context.Context, dir, toDir string, opts ...string) error {
	args := append([]string{
		"--seal",
		path.Join(dir, dataFile),
		path.Join(dir, idxFile),
	}, opts...)
	log.G(ctx).Debugf("%s %s", obdBinCommit, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, obdBinCommit, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to seal writable overlaybd: %s", out)
	}
	if err := os.Rename(path.Join(dir, dataFile), path.Join(toDir, sealedFile)); err != nil {
		return errors.Wrapf(err, "failed to rename sealed overlaybd file")
	}
	os.RemoveAll(path.Join(dir, idxFile))
	return nil
}

func Commit(ctx context.Context, dir, toDir string, sealed bool, opts ...string) error {
	var args []string
	if sealed {
		args = append([]string{
			"--commit_sealed",
			path.Join(dir, sealedFile),
			path.Join(toDir, commitTempFile),
		}, opts...)
	} else {
		args = append([]string{
			path.Join(dir, dataFile),
			path.Join(dir, idxFile),
			path.Join(toDir, commitFile),
		}, opts...)
	}
	log.G(ctx).Debugf("%s %s", obdBinCommit, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, obdBinCommit, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-commit: %s", out)
	}
	if sealed {
		return os.Rename(path.Join(toDir, commitTempFile), path.Join(toDir, commitFile))
	}
	return nil
}

func ApplyOverlaybd(ctx context.Context, dir string, opts ...string) error {

	args := append([]string{
		path.Join(dir, "layer.tar"),
		path.Join(dir, "config.json")}, opts...)
	log.G(ctx).Debugf("%s %s", obdBinApply, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, obdBinApply, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-apply[native]: %s", out)
	}
	return nil
}

func ApplyTurboOCI(ctx context.Context, dir, gzipMetaFile string, opts ...string) error {

	args := append([]string{
		path.Join(dir, "layer.tar"),
		path.Join(dir, "config.json"),
		"--gz_index_path", path.Join(dir, gzipMetaFile)}, opts...)
	log.G(ctx).Debugf("%s %s", obdBinApply, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, obdBinApply, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-apply[turboOCI]: %s", out)
	}
	return nil
}
