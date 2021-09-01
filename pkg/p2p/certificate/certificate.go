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

package certificate

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io/ioutil"
	"math/big"
	"net"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	defaultRootCA  *x509.Certificate
	defaultRootKey *rsa.PrivateKey
)

// GetRootCA get root CA, if not found will create
func GetRootCA(certPath, certKeyPath string, isGenerateCert bool) *tls.Certificate {
	success, cert, key := loadRootCA(certPath, certKeyPath)
	if !success {
		log.Warnf("Load Root CA fail!")
		if !isGenerateCert {
			log.Fatal("If you want generate Root CA, please add argument `--generate-cert`.")
		} else {
			cert, key = generateRootCA()
			log.Info("Generate new Root CA success!")
			saveRootCA(cert, key, certPath, certKeyPath)
			log.Info("Save Root CA success!")
		}
	} else {
		log.Info("Load Root CA success!")
	}
	parserRootCA(cert, key)
	keyPair, _ := tls.X509KeyPair(cert, key)
	return &keyPair
}

// loadRootCA load root CA from disk
func loadRootCA(certPath, certKeyPath string) (bool, []byte, []byte) {
	cert, err := ioutil.ReadFile(certPath)
	if err != nil {
		log.Warnf("Read file %s fail! %s", certPath, err)
		return false, nil, nil
	}
	key, err := ioutil.ReadFile(certKeyPath)
	if err != nil {
		log.Warnf("Read file %s fail! %s", certKeyPath, err)
		return false, nil, nil
	}
	return true, cert, key
}

// generateRootCA generate root CA
func generateRootCA() ([]byte, []byte) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		log.Fatalf("Generate private key fail! %s", err)
	}
	template := certificateTemplate(true, "dadip2p")
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		log.Fatalf("Create Root CA fail! %s", err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return cert, key
}

// saveRootCA save root CA to disk
func saveRootCA(cert, key []byte, certPath, certKeyPath string) {
	if err := ioutil.WriteFile(certPath, cert, 0755); err != nil {
		log.Fatalf("Save Root CA %s fail! %s", certPath, err)
	}
	if err := ioutil.WriteFile(certKeyPath, key, 0755); err != nil {
		log.Fatalf("Save Root CA key %s fail! %s", certKeyPath, err)
	}
}

// parserRootCA parser root CA
func parserRootCA(cert, key []byte) {
	certBlock, _ := pem.Decode(cert)
	keyBlock, _ := pem.Decode(key)
	if certBlock == nil {
		log.Fatalf("Decode Root CA block fail!")
	}
	if keyBlock == nil {
		log.Fatalf("Decode Root CA key block fail!")
	}
	var err error
	if defaultRootCA, err = x509.ParseCertificate(certBlock.Bytes); err != nil {
		log.Fatalf("Parser Root CA fail! %s", err)
	}
	if defaultRootKey, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err != nil {
		log.Fatalf("Parser Root CA key fail! %s", err)
	}
}

// GenerateCertificate generate certificate from host
func GenerateCertificate(host string) ([]byte, []byte) {
	template := certificateTemplate(false, host)
	derBytes, err := x509.CreateCertificate(rand.Reader, template, defaultRootCA, &defaultRootKey.PublicKey, defaultRootKey)
	if err != nil {
		log.Fatalf("Create certificate fail! %s", err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(defaultRootKey)})
	return cert, key
}

// certificateTemplate template for generate certificate
func certificateTemplate(isCA bool, host string) *x509.Certificate {
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Country:            []string{"CN"},
			Organization:       []string{"dadip2p"},
			OrganizationalUnit: []string{"dadip2p"},
			Locality:           []string{"HangZhou"},
			Province:           []string{"ZheJiang"},
			CommonName:         host,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if !isCA {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	return template
}
