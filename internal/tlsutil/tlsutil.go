// Package tlsutil creates self-signed identities and pinned TLS configs.
// Trust is by SHA-256 fingerprint of the peer certificate (no CA involved).
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"
)

// LoadOrCreate loads cert+key PEM files or creates a new ECDSA P-256 self-signed identity.
func LoadOrCreate(certPath, keyPath, cn string) (tls.Certificate, error) {
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		c.Leaf, _ = x509.ParseCertificate(c.Certificate[0])
		return c, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(certPath); statErr == nil {
			return tls.Certificate{}, fmt.Errorf("load %s: %w", certPath, err)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"Straw Log"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{cn, "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return LoadOrCreate(certPath, keyPath, cn)
}

func Fingerprint(raw []byte) []byte {
	s := sha256.Sum256(raw)
	return s[:]
}

func FingerprintHex(raw []byte) string { return hex.EncodeToString(Fingerprint(raw)) }

// PinnedClient returns a client config that presents own and accepts only a server whose
// certificate hashes to pin.
func PinnedClient(own tls.Certificate, pin []byte) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{own},
		InsecureSkipVerify: true, // replaced by VerifyConnection pinning below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("agent presented no certificate")
			}
			if subtle.ConstantTimeCompare(Fingerprint(cs.PeerCertificates[0].Raw), pin) != 1 {
				return errors.New("agent certificate fingerprint mismatch (possible MITM or agent was reinstalled; re-pair it)")
			}
			return nil
		},
	}
}
