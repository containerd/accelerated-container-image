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
	"database/sql"
	"os"
	"os/signal"

	"github.com/containerd/accelerated-container-image/cmd/convertor/builder"
	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	_ "github.com/go-sql-driver/mysql"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	repo      string
	user      string
	plain     bool
	tagInput  string
	tagOutput string
	dir       string
	oci       bool
	fastoci   string
	turboOCI  string
	overlaybd string
	dbstr     string
	dbType    string

	rootCmd = &cobra.Command{
		Use:   "convertor",
		Short: "An image conversion tool from oci image to overlaybd image.",
		Long:  "overlaybd convertor is a standalone userspace image conversion tool that helps converting oci images to overlaybd images",
		Run: func(cmd *cobra.Command, args []string) {
			tb := ""
			if overlaybd == "" && fastoci == "" && turboOCI == "" {
				if tagOutput == "" {
					logrus.Error("output-tag is required, you can specify it by [-o|--overlaybd|--turboOCI]")
					os.Exit(1)
				}
				overlaybd = tagOutput
			}
			if fastoci != "" {
				tb = fastoci
			}
			if turboOCI != "" {
				tb = turboOCI
			}

			ctx := context.Background()
			opt := builder.BuilderOptions{
				Ref:       repo + ":" + tagInput,
				Auth:      user,
				PlainHTTP: plain,
				WorkDir:   dir,
				OCI:       oci,
			}
			if overlaybd != "" {
				logrus.Info("building [Overlaybd - Native]  image...")
				opt.Engine = builder.Overlaybd
				opt.TargetRef = repo + ":" + overlaybd

				switch dbType {
				case "mysql":
					if dbstr == "" {
						logrus.Warnf("no db-str was provided, falling back to no deduplication")
					}
					db, err := sql.Open("mysql", dbstr)
					if err != nil {
						logrus.Errorf("failed to open the provided mysql db: %v", err)
						os.Exit(1)
					}
					opt.DB = database.NewSqlDB(db)
				case "":
				default:
					logrus.Warnf("db-type %s was provided but is not one of known db types. Available: mysql", dbType)
					logrus.Warnf("falling back to no deduplication")
				}

				builder, err := builder.NewOverlayBDBuilder(ctx, opt)
				if err != nil {
					logrus.Errorf("failed to create overlaybd builder: %v", err)
					os.Exit(1)
				}
				if err := builder.Build(ctx); err != nil {
					logrus.Errorf("failed to build overlaybd: %v", err)
					os.Exit(1)
				}
				logrus.Info("overlaybd build finished")
			}
			if tb != "" {
				logrus.Info("building [Overlaybd - Turbo OCIv1] image...")
				opt.Engine = builder.TurboOCI
				opt.TargetRef = repo + ":" + tb
				builder, err := builder.NewOverlayBDBuilder(ctx, opt)
				if err != nil {
					logrus.Errorf("failed to create TurboOCI builder: %v", err)
					os.Exit(1)
				}
				if err := builder.Build(ctx); err != nil {
					logrus.Errorf("failed to build TurboOCIv1 image: %v", err)
					os.Exit(1)
				}
				logrus.Info("TurboOCIv1 build finished")
			}
		},
	}
)

func init() {
	rootCmd.Flags().SortFlags = false
	rootCmd.Flags().StringVarP(&repo, "repository", "r", "", "repository for converting image (required)")
	rootCmd.Flags().StringVarP(&user, "username", "u", "", "user[:password] Registry user and password")
	rootCmd.Flags().BoolVarP(&plain, "plain", "", false, "connections using plain HTTP")
	rootCmd.Flags().StringVarP(&tagInput, "input-tag", "i", "", "tag for image converting from (required)")
	rootCmd.Flags().StringVarP(&tagOutput, "output-tag", "o", "", "tag for image converting to (required)")
	rootCmd.Flags().StringVarP(&dir, "dir", "d", "tmp_conv", "directory used for temporary data")
	rootCmd.Flags().BoolVarP(&oci, "oci", "", false, "export image with oci spec")
	rootCmd.Flags().StringVar(&fastoci, "fastoci", "", "build 'Overlaybd-Turbo OCIv1' format (deprecated)")

	rootCmd.Flags().StringVar(&turboOCI, "turboOCI", "", "build 'Overlaybd-Turbo OCIv1' format")
	rootCmd.Flags().StringVar(&overlaybd, "overlaybd", "", "build overlaybd format")
	rootCmd.Flags().StringVar(&dbstr, "db-str", "", "db str for overlaybd conversion")
	rootCmd.Flags().StringVar(&dbType, "db-type", "", "type of db to use for conversion deduplication. Available: mysql. Default none")

	rootCmd.MarkFlagRequired("repository")
	rootCmd.MarkFlagRequired("input-tag")
}

func main() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	go func() {
		<-sigChan
		os.Exit(0)
	}()

	rootCmd.Execute()
}
