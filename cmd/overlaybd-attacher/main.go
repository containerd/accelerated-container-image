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
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/containerd/accelerated-container-image/pkg/types"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

var (
	commitID string = "unknown"
)

func main() {
	app := &cli.App{
		Name:    "overlaybd-attacher",
		Usage:   "A CLI tool to attach/detach overlaybd devices",
		Version: commitID,
		Commands: []*cli.Command{
			{
				Name:  "attach",
				Usage: "Attach an overlaybd device with given ID and configuration",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "id",
						Usage:    "Device ID (snapshot ID)",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "config",
						Usage:    "Path to overlaybd configuration file (config.v1.json)",
						Required: true,
					},
					&cli.BoolFlag{
						Name:    "verbose",
						Usage:   "Enable verbose logging",
						Aliases: []string{"v"},
					},
				},
				Action: attachAction,
			},
			{
				Name:  "detach",
				Usage: "Detach an overlaybd device with given ID",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "id",
						Usage:    "Device ID (snapshot ID)",
						Required: true,
					},
					&cli.BoolFlag{
						Name:    "verbose",
						Usage:   "Enable verbose logging",
						Aliases: []string{"v"},
					},
				},
				Action: detachAction,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func attachAction(c *cli.Context) error {
	if c.Bool("verbose") {
		logrus.SetLevel(logrus.DebugLevel)
	}

	id := c.String("id")
	tenant := -1
	configPath := c.String("config")
	retryCount := 5

	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("config file does not exist: %s: %w", configPath, err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var config types.OverlayBDBSConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("invalid JSON config file: %w", err)
	}

	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for config: %w", err)
	}

	var resultFile string
	if config.ResultFile != "" {
		if filepath.IsAbs(config.ResultFile) {
			resultFile = config.ResultFile
		} else {
			resultFile = filepath.Join(filepath.Dir(absConfigPath), config.ResultFile)
		}
	} else {
		resultFile = filepath.Join(filepath.Dir(absConfigPath), "init-debug.log")
	}

	ctx := context.Background()
	params := snapshot.NewAttachDeviceParams(id, tenant, absConfigPath, resultFile)

	logrus.Infof("attaching device: id=%s, tenant=%d, config=%s, resultFile=%s", id, tenant, absConfigPath, resultFile)

	var devName string
	var lastErr error
	for attempt := 1; attempt <= retryCount; attempt++ {
		if attempt > 1 {
			logrus.Warnf("retry attempt %d/%d after error: %v", attempt, retryCount, lastErr)
			time.Sleep(1 * time.Second)
		}

		devName, lastErr = snapshot.AttachDevice(ctx, params)
		if lastErr == nil {
			fmt.Println(devName)
			logrus.Infof("device attached successfully: %s", devName)
			return nil
		}
	}

	return fmt.Errorf("failed to attach device after %d attempts: %w", retryCount, lastErr)
}

func detachAction(c *cli.Context) error {
	if c.Bool("verbose") {
		logrus.SetLevel(logrus.DebugLevel)
	}

	id := c.String("id")
	tenant := -1

	ctx := context.Background()

	logrus.Infof("detaching device: id=%s, tenant=%d", id, tenant)

	if err := snapshot.DetachDevice(ctx, id, tenant); err != nil {
		return fmt.Errorf("failed to detach device: %w", err)
	}

	logrus.Infof("device detached successfully")
	return nil
}
