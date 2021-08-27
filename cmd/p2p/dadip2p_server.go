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

	"github.com/alibaba/accelerated-container-image/pkg/p2p"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	rootCmd = &cobra.Command{
		Use:   "dadip2p_server",
		Short: "dadip2p_server",
		Long:  `dadip2p_server`,
		Run: func(cmd *cobra.Command, args []string) {
			config := p2p.InitConfig(cfgFile)
			p2p.CheckConfig(config)
			p2p.Execute(config, true)
		},
	}
	cfgFile string
)

func init() {
	rand.Seed(time.Now().Unix())
	// cfg
	rootCmd.Flags().StringVarP(&cfgFile, "cfgFile", "", "dadip2p.yaml", "ServerConfig file (default is dadip2p.yaml)")
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
