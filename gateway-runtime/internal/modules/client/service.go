package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"go-blockchain-api/internal/models"
	"golang.org/x/crypto/bcrypt"
)

type Service interface {
	RegisterClient(req CreateClientRequest) (*models.Client, string, error)
	GetClients() ([]models.Client, error)
	GetKafkaConfigs() ([]KafkaConfigWithClient, error)
	GetDashboardSummary() (DashboardSummary, error)
	ToggleClientStatus(id string) (*models.Client, error)
	DeleteClient(id string) error
	GetUsers() ([]AdminUserWithClient, error)
	GetUsersByClient(clientID string) ([]models.User, error)
	AddUserToClient(clientID string, username, password string) (*models.User, error)
	AddUser(clientID, username, password, fullName, role string) (*models.User, error)
	RemoveUser(userID string) error
	RegenerateAPIKey(id string) (string, error)
}

type KafkaConfigWithClient struct {
	models.ClientKafkaConfig
	CompanyName string `json:"company_name"`
}

type DashboardSummary struct {
	TotalClients      int64 `json:"total_clients"`
	ActiveClients     int64 `json:"active_clients"`
	InactiveClients   int64 `json:"inactive_clients"`
	PendingClients    int64 `json:"pending_clients"`
	ConfiguredClients int64 `json:"configured_clients"`
	TotalUsers        int64 `json:"total_users"`
	TotalStreams      int64 `json:"total_streams"`
	ActiveStreams     int64 `json:"active_streams"`
}

type clientService struct {
	repo Repository
}

func NewService(repo Repository) Service {
	return &clientService{repo: repo}
}

func (s *clientService) RegisterClient(req CreateClientRequest) (*models.Client, string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, "", errors.New("gagal generate API key yang aman")
	}

	rawAPIKey := "ak_live_" + hex.EncodeToString(randomBytes)

	hash := sha256.Sum256([]byte(rawAPIKey))
	apiKeyHash := hex.EncodeToString(hash[:])
	apiKeyPrefix := rawAPIKey[:16]

	status := req.Status
	if status == "" {
		status = "active"
	}

	newClient := &models.Client{
		CompanyName:        req.CompanyName,
		APIKeyHash:         apiKeyHash,
		APIKeyPrefix:       apiKeyPrefix,
		Status:             status,
		ActorField:         req.ActorField,
		FallbackActorField: req.FallbackActorField,
		CreateActorField:   req.CreateActorField,
		UpdateActorField:   req.UpdateActorField,
		DeleteActorField:   req.DeleteActorField,
		ActionField:        req.ActionField,
		ResourceField:      req.ResourceField,
	}

	if err := s.repo.CreateClient(newClient); err != nil {
		return nil, "", errors.New("gagal mendaftarkan klien ke database")
	}

	return newClient, rawAPIKey, nil
}

func (s *clientService) RegenerateAPIKey(id string) (string, error) {
	client, err := s.repo.GetClientByID(id)
	if err != nil {
		return "", errors.New("klien tidak ditemukan")
	}

	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", errors.New("gagal generate API key yang aman")
	}

	rawAPIKey := "ak_live_" + hex.EncodeToString(randomBytes)
	hash := sha256.Sum256([]byte(rawAPIKey))

	client.APIKeyHash = hex.EncodeToString(hash[:])
	client.APIKeyPrefix = rawAPIKey[:16]

	if err := s.repo.UpdateClient(client); err != nil {
		return "", errors.New("gagal mengupdate API key klien")
	}

	return rawAPIKey, nil
}

func (s *clientService) GetClients() ([]models.Client, error) {
	return s.repo.GetClients()
}

func (s *clientService) GetKafkaConfigs() ([]KafkaConfigWithClient, error) {
	configs, err := s.repo.GetKafkaConfigs()
	if err != nil {
		return nil, err
	}

	clients, err := s.repo.GetClients()
	if err != nil {
		return nil, err
	}

	clientMap := make(map[string]string)
	for _, c := range clients {
		clientMap[c.ID] = c.CompanyName
	}

	var result []KafkaConfigWithClient
	for _, cfg := range configs {
		companyName := clientMap[cfg.ClientID]
		if companyName == "" {
			companyName = "Unknown Client"
		}
		result = append(result, KafkaConfigWithClient{
			ClientKafkaConfig: cfg,
			CompanyName:       companyName,
		})
	}

	return result, nil
}

func (s *clientService) GetDashboardSummary() (DashboardSummary, error) {
	clients, err := s.repo.GetClients()
	if err != nil {
		return DashboardSummary{}, err
	}

	configs, err := s.repo.GetKafkaConfigs()
	if err != nil {
		return DashboardSummary{}, err
	}

	var activeStreams int64
	configuredClientIDs := make(map[string]bool)
	for _, cfg := range configs {
		if cfg.IsActive {
			activeStreams++
		}
		configuredClientIDs[cfg.ClientID] = true
	}

	var activeClients int64
	var inactiveClients int64
	var pendingClients int64
	for _, client := range clients {
		switch client.Status {
		case "active":
			activeClients++
		case "pending_setup":
			pendingClients++
		default:
			inactiveClients++
		}
	}

	totalUsers, err := s.repo.CountUsers()
	if err != nil {
		return DashboardSummary{}, err
	}

	return DashboardSummary{
		TotalClients:      int64(len(clients)),
		ActiveClients:     activeClients,
		InactiveClients:   inactiveClients,
		PendingClients:    pendingClients,
		ConfiguredClients: int64(len(configuredClientIDs)),
		TotalUsers:        totalUsers,
		TotalStreams:      int64(len(configs)),
		ActiveStreams:     activeStreams,
	}, nil
}

func (s *clientService) ToggleClientStatus(id string) (*models.Client, error) {
	client, err := s.repo.GetClientByID(id)
	if err != nil {
		return nil, err
	}

	newStatus := "active"
	if client.Status == "active" {
		newStatus = "inactive"
	}

	if err := s.repo.UpdateClientStatus(id, newStatus); err != nil {
		return nil, err
	}

	client.Status = newStatus
	return client, nil
}

func (s *clientService) DeleteClient(id string) error {
	return s.repo.DeleteClient(id)
}

func (s *clientService) GetUsers() ([]AdminUserWithClient, error) {
	return s.repo.GetUsers()
}

func (s *clientService) GetUsersByClient(clientID string) ([]models.User, error) {
	_, err := s.repo.GetClientByID(clientID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetUsersByClientID(clientID)
}

func (s *clientService) AddUserToClient(clientID string, username, password string) (*models.User, error) {
	return s.AddUser(clientID, username, password, "", "Auditor")
}

func (s *clientService) AddUser(clientID, username, password, fullName, role string) (*models.User, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "Auditor"
	}
	if !strings.EqualFold(role, "admin") && !strings.EqualFold(role, "auditor") {
		return nil, errors.New("invalid_role")
	}
	if strings.EqualFold(role, "admin") {
		role = "admin"
	} else {
		role = "Auditor"
	}

	clientID = strings.TrimSpace(clientID)
	if role == "Auditor" {
		if clientID == "" {
			return nil, errors.New("client_required")
		}
		_, err := s.repo.GetClientByID(clientID)
		if err != nil {
			return nil, err
		}
	} else {
		clientID = ""
	}

	var userClientID *string
	if clientID != "" {
		userClientID = &clientID
	}

	exists, err := s.repo.CheckUsernameExists(username)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New("username_already_exists")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	newUser := &models.User{
		ClientID: userClientID,
		FullName: fullName,
		Username: username,
		Password: string(hashedPassword),
		Role:     role,
	}

	if err := s.repo.CreateUser(newUser); err != nil {
		return nil, err
	}

	return newUser, nil
}

func (s *clientService) RemoveUser(userID string) error {
	return s.repo.DeleteUserByID(userID)
}
