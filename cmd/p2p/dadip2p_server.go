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
	"sync"
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
			if len(cfgFile) == 0 {
				_ = cmd.Help()
				return
			}
			config := configure.InitConfig(cfgFile)
			configure.CheckConfig(config)
			wg := &sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				server.StartProxyServer(config, true)
			}()
			go func() {
				defer wg.Done()
				server.StartP2PServer(config, true)
			}()
			wg.Wait()
		},
	}
	cfgFile string
)

func init() {
	// set seed
	rand.Seed(time.Now().UnixNano())
	// cfg
	rootCmd.Flags().StringVarP(&cfgFile, "cfgFile", "c", "", "config file path(template is dadip2p.yaml)")
	// flag
	rootCmd.Flags().BoolP("generate-cert", "", false, "is auto generate cert")
	bindFlag("ProxyConfig.CertConfig.GenerateCert", "generate-cert")
	rootCmd.Flags().StringP("log-level", "l", "info", "log level, debug | info | warn | error | panic")
	bindFlag("LogLevel", "log-level")
	rootCmd.Flags().IntP("proxy-port", "", 19245, "proxy server port")
	bindFlag("ProxyConfig.Port", "proxy-port")
	rootCmd.Flags().IntP("p2p-port", "", 19145, "p2p server port")
	bindFlag("P2PConfig.Port", "p2p-port")
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
