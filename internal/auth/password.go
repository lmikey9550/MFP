package auth

import "golang.org/x/crypto/bcrypt"

func HashPassword(password string) (string, error) {
	body, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func CheckPasswordHash(hash string, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
