package client

import (
	"go-blockchain-api/internal/models"
	"time"

	"gorm.io/gorm"
)

type Repository interface {
	CreateClient(client *models.Client) error
	GetClients() ([]models.Client, error)
	GetKafkaConfigs() ([]models.ClientKafkaConfig, error)
	UpdateClientStatus(id string, status string) error
	UpdateClient(client *models.Client) error
	DeleteClient(id string) error
	GetClientByID(id string) (*models.Client, error)
	GetUsers() ([]AdminUserWithClient, error)
	GetUsersByClientID(clientID string) ([]models.User, error)
	CountUsers() (int64, error)
	CreateUser(user *models.User) error
	DeleteUserByID(userID string) error
	CheckUsernameExists(username string) (bool, error)
}

type AdminUserWithClient struct {
	ID          string    `json:"id"`
	ClientID    string    `json:"client_id"`
	CompanyName string    `json:"company_name"`
	FullName    string    `json:"full_name"`
	Username    string    `json:"username"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type clientRepository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &clientRepository{db: db}
}

func (r *clientRepository) CreateClient(client *models.Client) error {
	return r.db.Create(client).Error
}

func (r *clientRepository) GetClients() ([]models.Client, error) {
	var clients []models.Client
	err := r.db.Order("created_at ASC").Find(&clients).Error
	return clients, err
}

func (r *clientRepository) GetKafkaConfigs() ([]models.ClientKafkaConfig, error) {
	var configs []models.ClientKafkaConfig
	err := r.db.Order("created_at ASC").Find(&configs).Error
	return configs, err
}

func (r *clientRepository) UpdateClientStatus(id string, status string) error {
	return r.db.Model(&models.Client{}).Where("id = ?", id).Update("status", status).Error
}

func (r *clientRepository) UpdateClient(client *models.Client) error {
	return r.db.Save(client).Error
}

func (r *clientRepository) DeleteClient(id string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&models.User{}, "client_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.ClientUser{}, "client_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.AgentConfig{}, "client_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.ClientKafkaConfig{}, "client_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Where("client_id = ?", id).Delete(&models.ClientTable{}).Error; err != nil {
			return err
		}

		result := tx.Delete(&models.Client{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (r *clientRepository) GetClientByID(id string) (*models.Client, error) {
	var client models.Client
	err := r.db.First(&client, "id = ?", id).Error
	return &client, err
}

func (r *clientRepository) GetUsers() ([]AdminUserWithClient, error) {
	var users []AdminUserWithClient
	err := r.db.Table("users").
		Select("users.id, COALESCE(users.client_id, '') AS client_id, COALESCE(clients.company_name, '') AS company_name, users.full_name, users.username, users.role, users.created_at, users.updated_at").
		Joins("LEFT JOIN clients ON clients.id = users.client_id").
		Where("users.deleted_at IS NULL").
		Order("users.created_at DESC").
		Scan(&users).Error
	return users, err
}

func (r *clientRepository) GetUsersByClientID(clientID string) ([]models.User, error) {
	var users []models.User
	err := r.db.Where("client_id = ?", clientID).Order("created_at DESC").Find(&users).Error
	return users, err
}

func (r *clientRepository) CountUsers() (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Count(&count).Error
	return count, err
}

func (r *clientRepository) CreateUser(user *models.User) error {
	err := r.db.Create(user).Error
	if err == nil || user.ClientID != nil {
		return err
	}

	if alterErr := r.db.Exec("ALTER TABLE users ALTER COLUMN client_id DROP NOT NULL").Error; alterErr != nil {
		return err
	}

	return r.db.Create(user).Error
}

func (r *clientRepository) DeleteUserByID(userID string) error {
	return r.db.Delete(&models.User{}, "id = ?", userID).Error
}

func (r *clientRepository) CheckUsernameExists(username string) (bool, error) {
	var count int64
	err := r.db.Model(&models.User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}
