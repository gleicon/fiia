// gen_certs generates a development root CA and hub TLS certificate.
// Idempotent: skips generation if all output files already exist.
// To rotate: go run ./dev/gen_certs --force
// Output: dev/ca/root_ca.pem, dev/ca/hub_cert.pem, dev/ca/hub_key.pem
//
// WARNING: these certificates are for local development only.
// They are committed to the repository and MUST NOT be used in production.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"time"
)

const (
	output_dir         = "dev/ca"
	ca_validity_years  = 10
	leaf_validity_days = 365
)

var cert_files = []string{
	output_dir + "/root_ca.pem",
	output_dir + "/hub_cert.pem",
	output_dir + "/hub_key.pem",
}

func main() {
	force := flag.Bool("force", false, "regenerate certs even if they already exist")
	flag.Parse()

	if err := os.MkdirAll(output_dir, 0755); err != nil {
		log.Fatalf("gen_certs: mkdir %q: %v", output_dir, err)
	}

	if !*force && certsExist() {
		log.Printf("gen_certs: certs already present in %s/ (use --force to rotate)", output_dir)
		return
	}

	ca_key, ca_cert, ca_cert_pem := generateRootCA()
	log.Printf("gen_certs: root CA generated (valid %d years)", ca_validity_years)

	hub_key_pem, hub_cert_pem := generateLeafCert(ca_key, ca_cert)
	log.Printf("gen_certs: hub cert generated (valid %d days)", leaf_validity_days)

	writePEM(output_dir+"/root_ca.pem", ca_cert_pem)
	writePEM(output_dir+"/hub_cert.pem", hub_cert_pem)
	writePEM(output_dir+"/hub_key.pem", hub_key_pem)

	log.Printf("gen_certs: written to %s/", output_dir)
}

func certsExist() bool {
	for _, f := range cert_files {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}
	return true
}

func generateRootCA() (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("gen_certs: generate root CA key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatalf("gen_certs: generate serial: %v", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Fiia Dev Root CA",
			Organization: []string{"Fiia Development"},
		},
		NotBefore:             now,
		NotAfter:              now.AddDate(ca_validity_years, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	cert_der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatalf("gen_certs: create root CA cert: %v", err)
	}

	cert, err := x509.ParseCertificate(cert_der)
	if err != nil {
		log.Fatalf("gen_certs: parse root CA cert: %v", err)
	}

	cert_pem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert_der})
	if len(cert_pem) == 0 {
		log.Fatal("gen_certs: encode root CA PEM: empty output")
	}

	return key, cert, cert_pem
}

func generateLeafCert(ca_key *ecdsa.PrivateKey, ca_cert *x509.Certificate) (key_pem, cert_pem []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("gen_certs: generate hub key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatalf("gen_certs: generate hub serial: %v", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "fiia-hub-dev",
			Organization: []string{"Fiia Development"},
		},
		NotBefore:   now,
		NotAfter:    now.AddDate(0, 0, leaf_validity_days),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost", "fiia-hub", "host.lima.internal"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	cert_der, err := x509.CreateCertificate(rand.Reader, tmpl, ca_cert, &key.PublicKey, ca_key)
	if err != nil {
		log.Fatalf("gen_certs: create hub cert: %v", err)
	}

	key_der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatalf("gen_certs: marshal hub key: %v", err)
	}

	cert_pem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert_der})
	key_pem = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key_der})

	if len(cert_pem) == 0 {
		log.Fatal("gen_certs: encode hub cert PEM: empty output")
	}
	if len(key_pem) == 0 {
		log.Fatal("gen_certs: encode hub key PEM: empty output")
	}

	return key_pem, cert_pem
}

func writePEM(path string, data []byte) {
	if len(data) == 0 {
		log.Fatalf("gen_certs: write %q: data must not be empty", path)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Fatalf("gen_certs: write %q: %v", path, err)
	}
	log.Printf("gen_certs: wrote %s (%d bytes)", path, len(data))
}
