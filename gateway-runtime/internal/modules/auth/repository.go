package auth

import (
	"go-blockchain-api/internal/models"

	"gorm.io/gorm"
)

type Repository interface {
	CheckClient(clientID string) (*models.Client, error)
	FindByID(userID string) (*models.User, error)
	FindByUsername(username string) (*models.User, error)
	IsUsernameTaken(username, excludeUserID string) (bool, error)
	CreateUser(user *models.User) error
	UpdateUser(user *models.User) error
}

type authRepository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &authRepository{db: db}
}

func (r *authRepository) CheckClient(clientID string) (*models.Client, error) {
	var client models.Client
	err := r.db.First(&client, "id = ?", clientID).Error
	return &client, err
}

func (r *authRepository) FindByID(userID string) (*models.User, error) {
	var user models.User
	err := r.db.First(&user, "id = ?", userID).Error
	return &user, err
}

func (r *authRepository) FindByUsername(username string) (*models.User, error) {
	var user models.User
	err := r.db.Where("username = ?", username).First(&user).Error
	return &user, err
}

func (r *authRepository) IsUsernameTaken(username, excludeUserID string) (bool, error) {
	var count int64
	err := r.db.Model(&models.User{}).
		Where("username = ? AND id <> ?", username, excludeUserID).
		Count(&count).Error
	return count > 0, err
}

func (r *authRepository) CreateUser(user *models.User) error {
	return r.db.Create(user).Error
}

func (r *authRepository) UpdateUser(user *models.User) error {
	return r.db.Save(user).Error
}
