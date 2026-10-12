package agent_audit

import (
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"auditchain-agent/internal/config"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type FabricReader struct {
	contract *client.Contract
	gateway  *client.Gateway
	conn     *grpc.ClientConn
}

func NewFabricReader(cfg config.FabricReadConfig) (*FabricReader, error) {
	if strings.TrimSpace(cfg.MSPID) == "" || strings.TrimSpace(cfg.PeerEndpoint) == "" ||
		strings.TrimSpace(cfg.TLSServerName) == "" || strings.TrimSpace(cfg.TLSCertPath) == "" || strings.TrimSpace(cfg.CertPath) == "" ||
		strings.TrimSpace(cfg.KeyPath) == "" || strings.TrimSpace(cfg.Channel) == "" ||
		strings.TrimSpace(cfg.Chaincode) == "" {
		return nil, errors.New("konfigurasi Fabric audit belum lengkap")
	}

	tlsPEM, err := os.ReadFile(filepath.Clean(cfg.TLSCertPath))
	if err != nil {
		return nil, fmt.Errorf("read Fabric TLS CA certificate: %w", err)
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(tlsPEM) {
		return nil, errors.New("Fabric TLS CA certificate tidak valid")
	}
	conn, err := grpc.NewClient(
		cfg.PeerEndpoint,
		grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(certPool, cfg.TLSServerName)),
	)
	if err != nil {
		return nil, fmt.Errorf("create Fabric gRPC connection: %w", err)
	}

	cleanupConn := true
	defer func() {
		if cleanupConn {
			_ = conn.Close()
		}
	}()

	certPEM, err := os.ReadFile(filepath.Clean(cfg.CertPath))
	if err != nil {
		return nil, fmt.Errorf("read Fabric client certificate: %w", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, errors.New("Fabric client certificate PEM tidak valid")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse Fabric client certificate: %w", err)
	}
	fabricIdentity, err := identity.NewX509Identity(cfg.MSPID, cert)
	if err != nil {
		return nil, fmt.Errorf("create Fabric identity: %w", err)
	}

	keyPEM, err := os.ReadFile(filepath.Clean(cfg.KeyPath))
	if err != nil {
		return nil, fmt.Errorf("read Fabric signing key: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("Fabric signing key PEM tidak valid")
	}
	privateKey, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse Fabric signing key: %w", err)
	}
	sign, err := identity.NewPrivateKeySign(privateKey)
	if err != nil {
		return nil, fmt.Errorf("create Fabric signer: %w", err)
	}

	gateway, err := client.Connect(
		fabricIdentity,
		client.WithSign(sign),
		client.WithClientConnection(conn),
		client.WithEvaluateTimeout(10*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect Fabric Gateway: %w", err)
	}
	cleanupConn = false
	contract := gateway.GetNetwork(cfg.Channel).GetContract(cfg.Chaincode)
	return &FabricReader{contract: contract, gateway: gateway, conn: conn}, nil
}

func parsePrivateKey(der []byte) (crypto.Signer, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
		return nil, errors.New("private key does not implement crypto.Signer")
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	return nil, errors.New("unsupported private key encoding")
}

func (f *FabricReader) QueryMerkleRoot(anchorID string) (string, error) {
	if f == nil || f.contract == nil {
		return "", errors.New("Fabric reader is not initialized")
	}
	result, err := f.contract.EvaluateTransaction("QueryMerkleRoot", anchorID)
	if err != nil {
		return "", fmt.Errorf("evaluate QueryMerkleRoot: %w", err)
	}
	var anchor struct {
		AnchorID      string `json:"anchor_id"`
		MerkleRoot    string `json:"merkle_root"`
		AlternateRoot string `json:"merkleRoot"`
	}
	if err := json.Unmarshal(result, &anchor); err != nil {
		return "", fmt.Errorf("decode Fabric anchor: %w", err)
	}
	if anchor.AnchorID != anchorID {
		return "", errors.New("Fabric returned a different anchor ID")
	}
	root := anchor.MerkleRoot
	if root == "" {
		root = anchor.AlternateRoot
	}
	if _, ok := normalizedRoot(root); !ok {
		return "", errors.New("Fabric anchor does not contain a valid Merkle root")
	}
	return root, nil
}

func (f *FabricReader) Close() {
	if f == nil {
		return
	}
	if f.gateway != nil {
		f.gateway.Close()
	}
	if f.conn != nil {
		_ = f.conn.Close()
	}
}
