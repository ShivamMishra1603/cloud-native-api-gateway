package auth

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
)

type Authenticator struct {
	cfg          *config.Config
	jwtPublicKey crypto.PublicKey
}

func NewAuthenticator(cfg *config.Config) (*Authenticator, error) {
	var jwtPublicKey crypto.PublicKey
	if cfg.Authentication.JWT.Enabled {
		keyBytes, err := os.ReadFile(cfg.Authentication.JWT.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("read jwt public key PEM file: %w", err)
		}
		block, _ := pem.Decode(keyBytes)
		if block == nil {
			return nil, fmt.Errorf("failed to decode PEM block from jwt public key")
		}
		pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			pubKey, err = x509.ParsePKCS1PublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse jwt public key: %w", err)
			}
		}
		jwtPublicKey = pubKey
	}

	return &Authenticator{
		cfg:          cfg,
		jwtPublicKey: jwtPublicKey,
	}, nil
}

// Authenticate checks the request against the service-level authentication rules.
// Returns consumer identification, HTTP status code, and error (if auth failed).
func (a *Authenticator) Authenticate(req *http.Request, regSvc *registry.Service) (string, int, error) {
	if !regSvc.Auth.Enabled {
		return "", http.StatusOK, nil
	}

	authType := strings.ToLower(strings.TrimSpace(regSvc.Auth.Type))
	if authType == "" {
		authType = "api_key"
	}

	var consumer string

	if authType == "api_key" {
		headerName := a.cfg.Authentication.APIKey.Header
		if headerName == "" {
			headerName = "X-API-Key"
		}

		apiKey := req.Header.Get(headerName)
		if apiKey == "" {
			return "", http.StatusUnauthorized, fmt.Errorf("Missing API Key")
		}

		var authenticated bool
		for _, record := range a.cfg.Authentication.APIKey.Keys {
			if record.Key == apiKey {
				consumer = record.Consumer
				authenticated = true
				break
			}
		}

		if !authenticated {
			return "", http.StatusUnauthorized, fmt.Errorf("Invalid API Key")
		}
	} else if authType == "jwt" {
		authHeader := req.Header.Get("Authorization")
		if authHeader == "" {
			return "", http.StatusUnauthorized, fmt.Errorf("Missing Authorization Header")
		}

		if !strings.HasPrefix(authHeader, "Bearer ") {
			return "", http.StatusUnauthorized, fmt.Errorf("Authorization header must start with Bearer")
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodRSA); ok {
				return a.jwtPublicKey, nil
			}
			if _, ok := token.Method.(*jwt.SigningMethodECDSA); ok {
				return a.jwtPublicKey, nil
			}
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		})

		if err != nil || !token.Valid {
			return "", http.StatusUnauthorized, fmt.Errorf("Invalid JWT: %v", err)
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			return "", http.StatusUnauthorized, fmt.Errorf("Invalid JWT claims")
		}

		sub, _ := claims["sub"].(string)
		if sub == "" {
			return "", http.StatusUnauthorized, fmt.Errorf("JWT sub claim is missing")
		}

		consumer = sub
	}

	// Validate allowed_consumers ACL
	if len(regSvc.Auth.AllowedConsumers) > 0 {
		authorized := false
		for _, allowed := range regSvc.Auth.AllowedConsumers {
			if allowed == consumer {
				authorized = true
				break
			}
		}
		if !authorized {
			return "", http.StatusForbidden, fmt.Errorf("Insufficient permissions")
		}
	}

	return consumer, http.StatusOK, nil
}
