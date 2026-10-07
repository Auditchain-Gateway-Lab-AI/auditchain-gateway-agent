package blockchain

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"go-blockchain-api/internal/models"

	"github.com/google/uuid"
	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gorm.io/gorm"
)

type FabricService struct {
	Contract *client.Contract
	gw       *client.Gateway
	conn     *grpc.ClientConn
	DB       *gorm.DB
}

// InitFabricGateway menginisialisasi koneksi ke jaringan Fabric
func InitFabricGateway(db *gorm.DB) (*FabricService, error) {
	mspID := os.Getenv("FABRIC_MSP_ID")
	peerEndpoint := os.Getenv("FABRIC_PEER_ENDPOINT")

	// 1. Load TLS Certificate untuk keamanan jalur gRPC
	tlsCertPath := os.Getenv("FABRIC_TLS_CERT_PATH")
	certPool := x509.NewCertPool()
	tlsCert, err := os.ReadFile(filepath.Clean(tlsCertPath))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca TLS cert: %v", err)
	}
	certPool.AppendCertsFromPEM(tlsCert)
	transportCredentials := credentials.NewClientTLSFromCert(certPool, "")

	conn, err := grpc.NewClient(peerEndpoint, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		return nil, fmt.Errorf("gagal membuat koneksi gRPC: %v", err)
	}

	// 2. Load Public Certificate (Identitas Node/Admin)
	certPath := os.Getenv("FABRIC_CERT_PATH")
	certBytes, err := os.ReadFile(filepath.Clean(certPath))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca sertifikat: %v", err)
	}
	certBlock, _ := pem.Decode(certBytes)
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("gagal parsing sertifikat: %v", err)
	}
	id, err := identity.NewX509Identity(mspID, cert)
	if err != nil {
		return nil, err
	}

	// 3. Load Private Key untuk Digital Signature (Tanda Tangan)
	keyPath := os.Getenv("FABRIC_KEY_PATH")
	keyBytes, err := os.ReadFile(filepath.Clean(keyPath))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca private key: %v", err)
	}
	keyBlock, _ := pem.Decode(keyBytes)
	privateKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		privateKey, err = x509.ParseECPrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("gagal parsing private key: %v", err)
		}
	}
	sign, err := identity.NewPrivateKeySign(privateKey)
	if err != nil {
		return nil, err
	}

	// 4. Buat Koneksi Gateway
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(conn),
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
	)
	if err != nil {
		return nil, err
	}

	network := gw.GetNetwork(os.Getenv("FABRIC_CHANNEL"))
	contract := network.GetContract(os.Getenv("FABRIC_CHAINCODE"))

	log.Println("✅ Terhubung ke Hyperledger Fabric Gateway!")

	return &FabricService{
		Contract: contract,
		gw:       gw,
		conn:     conn,
		DB:       db,
	}, nil
}

// AnchorPendingRoots mencari Merkle Root yang belum di-anchor dan mengirimnya ke Blockchain.
//
// CATATAN (diadopsi dari branch testing): setiap root yang berhasil di-anchor
// akan menandai SEMUA log dalam batch tersebut dengan blockchain_timestamp
// yang sama (waktu saat transaksi Fabric sukses di-submit). Ini memungkinkan
// pengukuran delta 3 titik: Timestamp (kejadian) → DBTimestamp (insert DB)
// → BlockchainTimestamp (anchor selesai), meski granularitasnya per-batch
// (bukan per-log seperti direct-anchoring), karena arsitektur Merkle Tree
// batch tetap dipertahankan di branch ini.
func (f *FabricService) AnchorPendingRoots() error {
	// Cari Merkle Root yang unik dari log berstatus AGGREGATED
	var distinctRoots []string
	f.DB.Model(&models.AuditLog{}).
		Where("status = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", "AGGREGATED").
		Distinct("merkle_root").Pluck("merkle_root", &distinctRoots)

	if len(distinctRoots) == 0 {
		return nil
	}

	for _, root := range distinctRoots {
		// Ambil metadata batch untuk Merkle Root ini
		var meta models.MerkleMetadata
		if err := f.DB.Where("merkle_root = ?", root).First(&meta).Error; err != nil {
			continue
		}

		anchorID := uuid.New().String()
		// FIX: gunakan RFC3339Nano (bukan RFC3339) agar presisi sub-detik
		// (microsecond/nanosecond) ikut tersimpan di ledger Fabric — bukan
		// hanya presisi detik. Chaincode StoreMerkleRoot menerima timestamp
		// sebagai string biasa, jadi perubahan format ini TIDAK memerlukan
		// redeploy/upgrade chaincode.
		anchorTime := time.Now()
		timestamp := anchorTime.Format(time.RFC3339Nano)
		sourceGateway := "AuditChain_Gateway_Node1"
		batchSizeStr := fmt.Sprintf("%d", meta.BatchSize)

		log.Printf("[Anchoring] Mengirim Merkle Root %s ke Fabric...", root)

		// Submit Transaksi ke Chaincode (Smart Contract)
		_, err := f.Contract.SubmitTransaction("StoreMerkleRoot", anchorID, root, timestamp, sourceGateway, batchSizeStr, "System_Signature")

		if err != nil {
			log.Printf("[Anchoring] ❌ Gagal mengirim ke Fabric untuk Root %s: %v\n", root, err)
			continue // Mekanisme retry sederhana: lewati dan coba lagi di siklus berikutnya
		}

		// Karena SDK Gateway v1.x mengabstraksi TxID, kita gunakan anchorID sebagai representasi transaksi (atau modifikasi chaincode untuk me-return TxID asli)
		blockchainTxID := anchorID

		// Update database: Tandai log sebagai ANCHORED, simpan TxID, dan catat
		// blockchain_timestamp untuk seluruh log dalam batch ini sekaligus.
		err = f.DB.Model(&models.AuditLog{}).
			Where("merkle_root = ?", root).
			Updates(map[string]interface{}{
				"status":               "ANCHORED",
				"blockchain_tx_id":     blockchainTxID,
				"blockchain_timestamp": anchorTime,
			}).Error

		if err == nil {
			log.Printf("[Anchoring] ✅ Sukses Anchoring! Root: %s | TxID: %s", root, blockchainTxID)
		}
	}

	return nil
}

// AnchorPendingRecoveryRoots anchors recovery evidence without touching
// client-originated audit_logs.
func (f *FabricService) AnchorPendingRecoveryRoots() error {
	var roots []string
	if err := f.DB.Model(&models.RecoveryEvent{}).
		Where("pipeline_status = ?", models.RecoveryPipelineAggregated).
		Distinct("merkle_root").Pluck("merkle_root", &roots).Error; err != nil {
		return err
	}
	for _, root := range roots {
		var meta models.MerkleMetadata
		if err := f.DB.Where("merkle_root = ?", root).First(&meta).Error; err != nil {
			continue
		}
		anchorID := uuid.New().String()
		anchorTime := time.Now().UTC()
		batchSize := fmt.Sprintf("%d", meta.BatchSize)
		_, err := f.Contract.SubmitTransaction("StoreMerkleRoot", anchorID, root, anchorTime.Format(time.RFC3339Nano), "AuditChain_Gateway_Node1", batchSize, "System_Signature")
		if err != nil {
			log.Printf("[Anchoring] recovery root %s gagal: %v", root, err)
			continue
		}
		if err := f.DB.Model(&models.RecoveryEvent{}).
			Where("merkle_root = ? AND pipeline_status = ?", root, models.RecoveryPipelineAggregated).
			Updates(map[string]interface{}{
				"pipeline_status":      models.RecoveryPipelineAnchored,
				"blockchain_tx_id":     anchorID,
				"blockchain_timestamp": anchorTime,
			}).Error; err != nil {
			return err
		}
		log.Printf("[Anchoring] recovery root sukses: %s | TxID: %s", root, anchorID)
	}
	return nil
}

// GetAnchorFromLedger menarik data Merkle Root asli yang tersimpan di dalam jaringan Fabric
func (f *FabricService) GetAnchorFromLedger(anchorID string) (string, error) {
	// Catatan: Kita menggunakan EvaluateTransaction, bukan SubmitTransaction
	resultBytes, err := f.Contract.EvaluateTransaction("QueryMerkleRoot", anchorID)
	if err != nil {
		return "", err
	}

	return string(resultBytes), nil
}

func (f *FabricService) Close() {
	if f.gw != nil {
		f.gw.Close()
	}
	if f.conn != nil {
		f.conn.Close()
	}
}
