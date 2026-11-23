package remote

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// The web CA signs two kinds of certificate:
//
//   - the HTTPS certificate the host's web UI presents through the tunnel
//     (for localhost and 127.0.0.1, where the browser reaches it);
//   - browser client certificates, which admins need to use their admin
//     powers in the web UI.
//
// It's name-constrained to localhost and loopback addresses, so trusting
// it in a browser can't let it vouch for any real website.

// WebCA is the host's web certificate authority.
type WebCA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
}

func webCAPaths(configDir string) (string, string) {
	d := filepath.Join(configDir, "remote")
	return filepath.Join(d, "web-ca.pem"), filepath.Join(d, "web-ca.key")
}

// WebCACertPath is the CA certificate users import into their browser.
func WebCACertPath(configDir string) string { c, _ := webCAPaths(configDir); return c }

// LoadOrCreateWebCA reads the web CA, creating it the first time.
func LoadOrCreateWebCA(configDir, hostname string) (*WebCA, error) {
	certPath, keyPath := webCAPaths(configDir)
	if cb, err := os.ReadFile(certPath); err == nil {
		kb, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		return parseCA(cb, kb)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	_, lo4, _ := net.ParseCIDR("127.0.0.0/8")
	_, lo6, _ := net.ParseCIDR("::1/128")
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{CommonName: "Anyserve web CA (" + hostname + ")", Organization: []string{"Anyserve"}},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.AddDate(10, 0, 0),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"localhost"},
		PermittedIPRanges:           []*net.IPNet{lo4, lo6},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	c, err := x509.ParseCertificate(der)
	return &WebCA{Cert: c, Key: key}, err
}

func parseCA(cb, kb []byte) (*WebCA, error) {
	cblk, _ := pem.Decode(cb)
	kblk, _ := pem.Decode(kb)
	if cblk == nil || kblk == nil {
		return nil, errors.New("web CA files are damaged")
	}
	c, err := x509.ParseCertificate(cblk.Bytes)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(kblk.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("web CA key isn't a signing key")
	}
	return &WebCA{Cert: c, Key: signer}, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

// PEM is the CA certificate in PEM form.
func (ca *WebCA) PEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw})
}

// ServerCert makes the web UI's HTTPS certificate, valid for localhost and
// loopback addresses.
func (ca *WebCA) ServerCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, 397),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: key}, nil
}

// ClientCert is a browser client certificate, ready to import.
type ClientCert struct {
	Serial   string
	NotAfter time.Time
	// P12 is a PKCS#12 bundle of the key and certificate, protected by
	// Password, which browsers import.
	P12      []byte
	Password string
}

// IssueClientCert issues a browser client certificate for a user.
func (ca *WebCA) IssueClientCert(user string, valid time.Duration, password string) (*ClientCert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sn := serial()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: user, Organization: []string{"Anyserve admin"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(valid),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	p12, err := pkcs12.Modern.Encode(key, cert, []*x509.Certificate{ca.Cert}, password)
	if err != nil {
		return nil, err
	}
	return &ClientCert{Serial: hex.EncodeToString(sn.Bytes()), NotAfter: cert.NotAfter, P12: p12, Password: password}, nil
}

// TLSConfig is the web UI's TLS setup: it asks for a client certificate
// and checks any it gets against the CA, but doesn't require one (users
// without one get their ordinary view).
func (ca *WebCA) TLSConfig() (*tls.Config, error) {
	sc, err := ca.ServerCert()
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return &tls.Config{
		Certificates: []tls.Certificate{sc},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// CertFor reports whether a TLS connection carried a valid client
// certificate issued to user and not revoked (its serial is still in
// serials). The TLS handshake has already checked the CA, the expiry and
// the client-auth usage.
func CertFor(state tls.ConnectionState, user string, serials []string) bool {
	if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return false
	}
	c := state.PeerCertificates[0]
	if c.Subject.CommonName != user {
		return false
	}
	return slices.Contains(serials, hex.EncodeToString(c.SerialNumber.Bytes()))
}

// Describe summarises a CA for messages.
func (ca *WebCA) Describe() string {
	return fmt.Sprintf("%s (valid until %s)", ca.Cert.Subject.CommonName, ca.Cert.NotAfter.Format("2006-01-02"))
}
