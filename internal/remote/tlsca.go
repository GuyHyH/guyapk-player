package remote

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// authority is this computer's private certificate authority. It is limited by
// X.509 name constraints to private LAN addresses, so even a stolen key cannot
// impersonate any public website.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func privateRanges() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func loadOrCreateCA(dir string) (*authority, error) {
	certFile, keyFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if a, err := loadCA(certFile, keyFile); err == nil {
		return a, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:                sn,
		Subject:                     pkix.Name{CommonName: "GuyAPK Remote Local CA", Organization: []string{"GuyAPK"}},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.AddDate(5, 0, 0),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedIPRanges:           privateRanges(),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &authority{cert, key}, nil
}

func loadCA(certFile, keyFile string) (*authority, error) {
	cb, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	kb, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	cp, _ := pem.Decode(cb)
	kp, _ := pem.Decode(kb)
	if cp == nil || kp == nil {
		return nil, errors.New("bad pem")
	}
	cert, err := x509.ParseCertificate(cp.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(kp.Bytes)
	if err != nil {
		return nil, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) || time.Now().After(cert.NotAfter.AddDate(0, -1, 0)) {
		return nil, errors.New("unusable ca")
	}
	return &authority{cert, key}, nil
}

// issue creates a server certificate for ip that meets Apple's TLS rules.
func (a *authority) issue(ip net.IP) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	sn, err := serial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "GuyAPK Remote", Organization: []string{"GuyAPK"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, 397),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{ip},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key}, nil
}

func (a *authority) fingerprint() string {
	s := sha256.Sum256(a.cert.Raw)
	return strings.ToUpper(hex.EncodeToString(s[:]))
}

func stableUUID(seed []byte, label string) string {
	s := sha256.Sum256(append(append([]byte{}, seed...), label...))
	s[6], s[8] = s[6]&0x0f|0x40, s[8]&0x3f|0x80
	h := strings.ToUpper(hex.EncodeToString(s[:16]))
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// mobileconfig wraps the CA in an Apple configuration profile.
func (a *authority) mobileconfig() []byte {
	inner, outer := stableUUID(a.cert.Raw, "cert"), stableUUID(a.cert.Raw, "profile")
	var b bytes.Buffer
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>PayloadContent</key><array><dict>
<key>PayloadCertificateFileName</key><string>GuyAPK-Remote.cer</string>
<key>PayloadContent</key><data>%s</data>
<key>PayloadDescription</key><string>Secures the connection between this iPad and GuyAPK on your computer.</string>
<key>PayloadDisplayName</key><string>GuyAPK Remote Local CA</string>
<key>PayloadIdentifier</key><string>th.guyapk.remote.ca.%s</string>
<key>PayloadType</key><string>com.apple.security.root</string>
<key>PayloadUUID</key><string>%s</string>
<key>PayloadVersion</key><integer>1</integer>
</dict></array>
<key>PayloadDescription</key><string>เชื่อมต่อ iPad กับ GuyAPK บนคอมพิวเตอร์อย่างปลอดภัย ใช้ได้เฉพาะที่อยู่ในเครือข่ายภายใน</string>
<key>PayloadDisplayName</key><string>GuyAPK Remote</string>
<key>PayloadIdentifier</key><string>th.guyapk.remote.%s</string>
<key>PayloadOrganization</key><string>GuyAPK</string>
<key>PayloadRemovalDisallowed</key><false/>
<key>PayloadType</key><string>Configuration</string>
<key>PayloadUUID</key><string>%s</string>
<key>PayloadVersion</key><integer>1</integer>
</dict></plist>
`, base64.StdEncoding.EncodeToString(a.cert.Raw), inner, inner, outer, outer)
	return b.Bytes()
}
