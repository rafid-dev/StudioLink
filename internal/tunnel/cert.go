package tunnel

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
	"path/filepath"
	"strings"
	"time"
)

const (
	certFile = "relay-cert.pem"
	keyFile  = "relay-key.pem"
)

// Fingerprint identifies a certificate by the SHA-256 of its DER bytes, in
// the form "sha256:<64 hex digits>".
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidFingerprint reports whether s is in the form Fingerprint produces.
// Upper-case hex and colon-separated pairs are accepted too.
func ValidFingerprint(s string) bool {
	_, err := parseFingerprint(s)
	return err == nil
}

func parseFingerprint(s string) ([]byte, error) {
	hexPart, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(s)), "sha256:")
	if !ok {
		return nil, errors.New(`fingerprint must start with "sha256:"`)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(hexPart, ":", ""))
	if err != nil || len(b) != sha256.Size {
		return nil, errors.New(`fingerprint must be "sha256:" followed by 64 hex digits`)
	}
	return b, nil
}

// PinnedTLS returns a client TLS config that trusts exactly the certificate
// with the given fingerprint, whatever its names or issuer. This is how the
// host trusts a relay's self-signed certificate.
func PinnedTLS(fingerprint string) (*tls.Config, error) {
	want, err := parseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Names and issuers are irrelevant with a pin; VerifyConnection
		// below does the real check.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("relay sent no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return fmt.Errorf("relay certificate is %s, not the pinned fingerprint; if the relay's certificate was replaced, update the fingerprint in studiolink.json", Fingerprint(cs.PeerCertificates[0].Raw))
			}
			return nil
		},
	}, nil
}

// LoadOrCreateCert loads the relay's self-signed certificate from dir,
// creating one on first use. The key file is readable only by its owner.
func LoadOrCreateCert(dir string) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("relay certificate in %s: %w", dir, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "studiolink relay"},
		NotBefore:    time.Now().Add(-time.Hour),
		// Pinned, so expiry only forces a needless re-pin; make it long.
		NotAfter:    time.Now().AddDate(20, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// CertFingerprint returns the fingerprint of a loaded certificate's leaf.
func CertFingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	return Fingerprint(cert.Certificate[0])
}
