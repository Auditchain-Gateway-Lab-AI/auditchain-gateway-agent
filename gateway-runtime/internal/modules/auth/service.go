package auth

import (
	"errors"
	"os"
	"strings"
	"time"

	"go-blockchain-api/internal/models"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Service interface {
	Register(clientID, username, password string) (*models.User, *models.Client, error)
	Login(username, password string) (string, error)
	GetProfile(userID string) (*models.User, *models.Client, error)
	UpdateProfile(userID, fullName, username, currentPassword, newPassword string) (*models.User, *models.Client, string, error)
}

type authService struct {
	repo Repository
}

func userClientID(user *models.User) string {
	if user == nil || user.ClientID == nil {
		return ""
	}
	return strings.TrimSpace(*user.ClientID)
}

func NewService(repo Repository) Service {
	return &authService{repo: repo}
}

func (s *authService) Register(clientID, username, password string) (*models.User, *models.Client, error) {
	client, err := s.repo.CheckClient(clientID)
	if err != nil {
		return nil, nil, errors.New("client_not_found")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, nil, errors.New("hash_error")
	}

	newUser := &models.User{
		ClientID: &clientID,
		Username: username,
		Password: string(hashedPassword),
		Role:     "Auditor",
	}

	if err := s.repo.CreateUser(newUser); err != nil {
		return nil, nil, errors.New("username_used")
	}

	return newUser, client, nil
}

func (s *authService) Login(username, password string) (string, error) {
	user, err := s.repo.FindByUsername(username)
	if err != nil {
		return "", errors.New("invalid_credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return "", errors.New("invalid_credentials")
	}

	// Periksa status klien untuk user non-Admin
	if !strings.EqualFold(user.Role, "admin") {
		client, err := s.repo.CheckClient(userClientID(user))
		if err != nil || client.Status != "active" {
			return "", errors.New("client_inactive")
		}
	}

	return s.issueToken(user)
}

func (s *authService) GetProfile(userID string) (*models.User, *models.Client, error) {
	user, err := s.repo.FindByID(userID)
	if err != nil {
		return nil, nil, errors.New("user_not_found")
	}

	if strings.EqualFold(user.Role, "admin") && userClientID(user) == "" {
		return user, nil, nil
	}

	client, err := s.repo.CheckClient(userClientID(user))
	if err != nil {
		return nil, nil, errors.New("client_not_found")
	}

	return user, client, nil
}

func (s *authService) UpdateProfile(userID, fullName, username, currentPassword, newPassword string) (*models.User, *models.Client, string, error) {
	user, client, err := s.GetProfile(userID)
	if err != nil {
		return nil, nil, "", err
	}

	username = strings.TrimSpace(username)
	fullName = strings.TrimSpace(fullName)
	if username == "" {
		return nil, nil, "", errors.New("username_required")
	}
	if len(username) < 4 {
		return nil, nil, "", errors.New("username_too_short")
	}

	taken, err := s.repo.IsUsernameTaken(username, userID)
	if err != nil {
		return nil, nil, "", errors.New("lookup_failed")
	}
	if taken {
		return nil, nil, "", errors.New("username_used")
	}

	if newPassword != "" {
		if len(newPassword) < 6 {
			return nil, nil, "", errors.New("password_too_short")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPassword)); err != nil {
			return nil, nil, "", errors.New("invalid_current_password")
		}
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return nil, nil, "", errors.New("hash_error")
		}
		user.Password = string(hashedPassword)
	}

	user.FullName = fullName
	user.Username = username
	if err := s.repo.UpdateUser(user); err != nil {
		return nil, nil, "", errors.New("update_failed")
	}

	token, err := s.issueToken(user)
	if err != nil {
		return nil, nil, "", err
	}

	return user, client, token, nil
}

func (s *authService) issueToken(user *models.User) (string, error) {
	claims := jwt.MapClaims{
		"user_id":   user.ID,
		"client_id": userClientID(user),
		"full_name": user.FullName,
		"username":  user.Username,
		"role":      user.Role,
		"exp":       time.Now().Add(time.Hour * 2).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	secret := os.Getenv("JWT_SECRET")

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", errors.New("token_error")
	}

	return tokenString, nil
}
