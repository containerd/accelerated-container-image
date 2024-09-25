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
	commitID         string = "unknown"
	repo             string
	user             string
	plain            bool
	tagInput         string
	digestInput      string
	tagOutput        string
	dir              string
	oci              bool
	fsType           string
	mkfs             bool
	verbose          bool
	vsize            int
	fastoci          string
	turboOCI         string
	overlaybd        string
	dbstr            string
	dbType           string
	concurrencyLimit int
	disableSparse    bool
	referrer         bool

	// certification
	certDirs    []string
	rootCAs     []string
	clientCerts []string
	insecure    bool
	// debug
	reserve      bool
	noUpload     bool
	dumpManifest bool

	rootCmd = &cobra.Command{
		Use:   "convertor",
		Short: "An image conversion tool from oci image to overlaybd image.",
		Long: `
Description: overlaybd convertor is a standalone userspace image conversion tool that helps converting oci images to overlaybd images.

Version: ` + commitID,
		Run: func(cmd *cobra.Command, args []string) {
			if verbose {
				logrus.SetLevel(logrus.DebugLevel)
			}
			tb := ""
			if digestInput == "" && tagInput == "" {
				logrus.Error("one of input-tag [-i] or input-digest [-g] is required")
				os.Exit(1)
			}
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

			if referrer {
				oci = true
			}

			ctx := context.Background()
			ref := repo + ":" + tagInput
			if tagInput == "" {
				ref = repo + "@" + digestInput
			}
			opt := builder.BuilderOptions{
				Ref:       ref,
				Auth:      user,
				PlainHTTP: plain,
				WorkDir:   dir,
				OCI:       oci,
				FsType:    fsType,
				Mkfs:      mkfs,
				Vsize:     vsize,
				CertOption: builder.CertOption{
					CertDirs:    certDirs,
					RootCAs:     rootCAs,
					ClientCerts: clientCerts,
					Insecure:    insecure,
				},
				Reserve:          reserve,
				NoUpload:         noUpload,
				DumpManifest:     dumpManifest,
				ConcurrencyLimit: concurrencyLimit,
				DisableSparse:    disableSparse,
				Referrer:         referrer,
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

				if err := builder.Build(ctx, opt); err != nil {
					logrus.Errorf("failed to build overlaybd: %v", err)
					os.Exit(1)
				}
				logrus.Info("overlaybd build finished")
			}
			if tb != "" {
				logrus.Info("building [Overlaybd - Turbo OCIv1] image...")
				opt.Engine = builder.TurboOCI
				opt.TargetRef = repo + ":" + tb
				if err := builder.Build(ctx, opt); err != nil {
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
	rootCmd.Flags().BoolVarP(&verbose, "verbose", "", false, "show debug log")
	rootCmd.Flags().StringVarP(&tagInput, "input-tag", "i", "", "tag for image converting from (required when input-digest is not set)")
	rootCmd.Flags().StringVarP(&digestInput, "input-digest", "g", "", "digest for image converting from (required when input-tag is not set)")
	rootCmd.Flags().StringVarP(&tagOutput, "output-tag", "o", "", "tag for image converting to")
	rootCmd.Flags().StringVarP(&dir, "dir", "d", "tmp_conv", "directory used for temporary data")
	rootCmd.Flags().BoolVarP(&oci, "oci", "", false, "export image with oci spec")
	rootCmd.Flags().StringVar(&fsType, "fstype", "ext4", "filesystem type of converted image.")
	rootCmd.Flags().BoolVarP(&mkfs, "mkfs", "", true, "make ext4 fs in bottom layer")
	rootCmd.Flags().IntVarP(&vsize, "vsize", "", 64, "virtual block device size (GB)")
	rootCmd.Flags().StringVar(&fastoci, "fastoci", "", "build 'Overlaybd-Turbo OCIv1' format (old name of turboOCIv1. deprecated)")
	rootCmd.Flags().StringVar(&turboOCI, "turboOCI", "", "build 'Overlaybd-Turbo OCIv1' format")
	rootCmd.Flags().StringVar(&overlaybd, "overlaybd", "", "build overlaybd format")
	rootCmd.Flags().StringVar(&dbstr, "db-str", "", "db str for overlaybd conversion")
	rootCmd.Flags().StringVar(&dbType, "db-type", "", "type of db to use for conversion deduplication. Available: mysql. Default none")
	rootCmd.Flags().IntVar(&concurrencyLimit, "concurrency-limit", 4, "the number of manifests that can be built at the same time, used for multi-arch images, 0 means no limit")
	rootCmd.Flags().BoolVar(&disableSparse, "disable-sparse", false, "disable sparse file for overlaybd")
	rootCmd.Flags().BoolVar(&referrer, "referrer", false, "push converted manifests with subject, note '--oci' will be enabled automatically if '--referrer' is set, cause the referrer must be in OCI format.")

	// certification
	rootCmd.Flags().StringArrayVar(&certDirs, "cert-dir", nil, "In these directories, root CA should be named as *.crt and client cert should be named as *.cert, *.key")
	rootCmd.Flags().StringArrayVar(&rootCAs, "root-ca", nil, "root CA certificates")
	rootCmd.Flags().StringArrayVar(&clientCerts, "client-cert", nil, "client cert certificates, should form in ${cert-file}:${key-file}")
	rootCmd.Flags().BoolVarP(&insecure, "insecure", "", false, "don't verify the server's certificate chain and host name")

	// debug
	rootCmd.Flags().BoolVar(&reserve, "reserve", false, "reserve tmp data")
	rootCmd.Flags().BoolVar(&noUpload, "no-upload", false, "don't upload layer and manifest")
	rootCmd.Flags().BoolVar(&dumpManifest, "dump-manifest", false, "dump manifest")

	rootCmd.MarkFlagRequired("repository")
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
