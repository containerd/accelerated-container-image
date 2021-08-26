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
	"math/rand"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/server"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/configure"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	rootCmd = &cobra.Command{
		Use:   "dadi-p2p",
		Short: "Dadi p2p is a file distribution service using P2P protocol.",
		Long:  `Dadi p2p is a file distribution service using P2P protocol. The main purpose is to accelerate the image's layer download.`,
		Run: func(cmd *cobra.Command, args []string) {
			config := configure.InitConfig(cfgFile)
			configure.CheckConfig(config)
			server.Execute(config, true)
		},
	}
	cfgFile string
)

func init() {
	// set seed
	rand.Seed(time.Now().UnixNano())
	// cfg
	rootCmd.Flags().StringVarP(&cfgFile, "cfgFile", "c", "dadip2p.yaml", "Config file (default is dadip2p.yaml)")
	// flag
	rootCmd.Flags().BoolP("generate-cert", "", false, "Is auto generate cert")
	bindFlag("CertConfig.GenerateCert", "generate-cert")
	rootCmd.Flags().StringP("log-level", "l", "info", "Log level, debug | info | warn | error | panic")
	bindFlag("LogLevel", "log-level")
	rootCmd.Flags().IntP("port", "p", 19145, "Port")
	bindFlag("Port", "port")
}

func bindFlag(configName, flagName string) {
	if err := viper.BindPFlag(configName, rootCmd.Flags().Lookup(flagName)); err != nil {
		log.Fatalf("Bind flag error! config name: %s, flag name: %s. %s", configName, flagName, err)
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Execute failed! %s", err)
	}
}
