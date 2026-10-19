package mock

import (
	"encoding/json"
	"errors"
	"sync"
)

// FabricServiceMock mensimulasikan FabricService untuk keperluan unit testing
// tanpa membutuhkan koneksi ke jaringan Hyperledger Fabric yang sebenarnya
type FabricServiceMock struct {
	mu         sync.RWMutex
	ledger     map[string]string // anchorID -> JSON payload
	ShouldFail bool              // set true untuk simulasi error Fabric
}

func NewFabricServiceMock() *FabricServiceMock {
	return &FabricServiceMock{
		ledger: make(map[string]string),
	}
}

// StoreAnchor menyimpan data ke ledger palsu (dipakai dalam test setup)
func (m *FabricServiceMock) StoreAnchor(anchorID, merkleRoot string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	payload := map[string]string{"merkle_root": merkleRoot}
	data, _ := json.Marshal(payload)
	m.ledger[anchorID] = string(data)
}

// GetAnchorFromLedger mengambil data dari ledger palsu
func (m *FabricServiceMock) GetAnchorFromLedger(anchorID string) (string, error) {
	if m.ShouldFail {
		return "", errors.New("mock: fabric connection failed")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	data, ok := m.ledger[anchorID]
	if !ok {
		return "", errors.New("mock: anchor not found")
	}
	return data, nil
}

// AnchorPendingRoots adalah no-op di mock — tidak melakukan apa-apa
func (m *FabricServiceMock) AnchorPendingRoots() error {
	if m.ShouldFail {
		return errors.New("mock: anchor failed")
	}
	return nil
}
