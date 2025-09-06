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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/containerd/accelerated-container-image/pkg/metrics"
	overlaybd "github.com/containerd/accelerated-container-image/pkg/snapshot"

	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	"github.com/containerd/containerd/v2/contrib/snapshotservice"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
)

const defaultConfigPath = "/etc/overlaybd-snapshotter/config.json"

var pconfig *overlaybd.BootConfig
var commitID string = "unknown"

func parseConfig(fpath string) error {
	logrus.Info("parse config file: ", fpath)
	data, err := os.ReadFile(fpath)
	if err != nil {
		return fmt.Errorf("failed to read plugin config from %s: %w", fpath, err)
	}
	if err := json.Unmarshal(data, pconfig); err != nil {
		return fmt.Errorf("failed to parse plugin config from %s: %w", string(data), err)
	}
	logrus.Infof("snapshotter commitID: %s, rwMode: %s, autoRemove: %v, writableLayerType: %s, asyncRemoveSnapshot: %v",
		commitID, pconfig.RwMode, pconfig.AutoRemoveDev, pconfig.WritableLayerType, pconfig.AsyncRemove)
	return nil
}

// TODO: use github.com/urfave/cli/v2
func main() {
	pconfig = overlaybd.DefaultBootConfig()
	fnConfig := defaultConfigPath
	if len(os.Args) == 2 {
		fnConfig = os.Args[1]
	}
	if err := parseConfig(fnConfig); err != nil {
		logrus.Error(err)
		os.Exit(1)
	}
	if pconfig.LogReportCaller {
		logrus.SetReportCaller(true)
	}

	metrics.Config = pconfig.ExporterConfig
	if pconfig.ExporterConfig.Enable {
		go metrics.Init()
		logrus.Infof("set Prometheus metrics exporter in http://localhost:%d%s", metrics.Config.Port, metrics.Config.UriPrefix)
	}
	contain := func(fsType string) bool {
		for _, fst := range pconfig.TurboFsType {
			if fst == fsType {
				return true
			}
		}
		return false
	}
	if !contain("ext4") {
		pconfig.TurboFsType = append(pconfig.TurboFsType, "ext4")
	}
	if !contain("erofs") {
		pconfig.TurboFsType = append(pconfig.TurboFsType, "erofs")
	}

	if err := setLogLevel(pconfig.LogLevel); err != nil {
		logrus.Errorf("failed to set log level: %v", err)
	} else {
		logrus.Infof("set log level: %s", pconfig.LogLevel)
	}

	sn, err := overlaybd.NewSnapshotter(pconfig)
	if err != nil {
		logrus.Errorf("failed to init overlaybd snapshotter: %v", err)
		os.Exit(1)
	}
	defer sn.Close()

	srv := grpc.NewServer()
	snapshotsapi.RegisterSnapshotsServer(srv, snapshotservice.FromSnapshotter(sn))

	address := strings.TrimSpace(pconfig.Address)

	if address == "" {
		logrus.Errorf("invalid address path(%s)", address)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(address), 0700); err != nil {
		logrus.Errorf("failed to create directory %v", filepath.Dir(address))
		os.Exit(1)
	}

	// try to remove the socket file to avoid EADDRINUSE
	if err := os.RemoveAll(address); err != nil {
		logrus.Errorf("failed to remove %v", address)
		os.Exit(1)
	}

	l, err := net.Listen("unix", address)
	if err != nil {
		logrus.Errorf("failed to listen on %s: %v", address, err)
		os.Exit(1)
	}

	go func() {
		if err := srv.Serve(l); err != nil {
			logrus.Errorf("failed to server: %v", err)
			os.Exit(1)
		}
	}()
	logrus.Infof("start to serve overlaybd snapshotter on %s", address)

	signals := make(chan os.Signal, 32)
	signal.Notify(signals, unix.SIGTERM, unix.SIGINT, unix.SIGPIPE)

	<-handleSignals(context.TODO(), signals, srv)

	if pconfig.ExporterConfig.Enable {
		metrics.IsAlive.Set(0)
	}
}

func handleSignals(ctx context.Context, signals chan os.Signal, server *grpc.Server) chan struct{} {
	doneCh := make(chan struct{}, 1)

	go func() {
		for {
			s := <-signals
			switch s {
			case unix.SIGUSR1:
				dumpStacks()
			case unix.SIGPIPE:
				continue
			default:
				if server == nil {
					close(doneCh)
					return
				}

				server.GracefulStop()
				close(doneCh)
				return
			}
		}
	}()

	return doneCh
}

func dumpStacks() {
	var (
		buf       []byte
		stackSize int
	)

	bufferLen := 16384
	for stackSize == len(buf) {
		buf = make([]byte, bufferLen)
		stackSize = runtime.Stack(buf, true)
		bufferLen *= 2
	}

	buf = buf[:stackSize]
	logrus.Infof("=== BEGIN goroutine stack dump ===\n%s\n=== END goroutine stack dump ===", buf)
}

func setLogLevel(level string) error {
	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		return err
	}
	logrus.SetLevel(logLevel)
	return nil
}
