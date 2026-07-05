package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/server"
)

func TestAuthenticationIntegration(t *testing.T) {
	// 1. Generate an RSA Keypair for JWT tests
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	pubDer, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal public key: %v", err)
	}
	pubBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDer,
	}

	tmpFile, err := os.CreateTemp("", "jwt_public_*.pem")
	if err != nil {
		t.Fatalf("failed to create temp public key file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if err := pem.Encode(tmpFile, pubBlock); err != nil {
		t.Fatalf("failed to write public key to PEM file: %v", err)
	}

	// 2. Set up the mock upstream (backend) server
	var lastReceivedConsumer string
	var lastReceivedPath string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastReceivedConsumer = r.Header.Get("X-Consumer")
		lastReceivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-response"))
	}))
	defer backend.Close()

	// 3. Configure the API Gateway with global and service auth parameters
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		Authentication: config.AuthenticationConfig{
			APIKey: config.APIKeyConfig{
				Enabled: true,
				Header:  "X-API-Key",
				Keys: []config.APIKeyRecord{
					{Key: "alice-secret-key", Consumer: "alice"},
					{Key: "bob-secret-key", Consumer: "bob"},
				},
			},
			JWT: config.JWTConfig{
				Enabled:   true,
				PublicKey: tmpFile.Name(),
			},
		},
		Services: []config.ServiceConfig{
			{
				Name: "api-key-service",
				Auth: config.AuthConfig{
					Enabled:          true,
					Type:             "api_key",
					AllowedConsumers: []string{"alice"}, // Restricted to alice
				},
				Routes: []config.RouteConfig{
					{Path: "/secure/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
			{
				Name: "jwt-service",
				Auth: config.AuthConfig{
					Enabled:          true,
					Type:             "jwt",
					AllowedConsumers: []string{"alice"}, // Restricted to alice
				},
				Routes: []config.RouteConfig{
					{Path: "/jwt/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
			{
				Name: "public-service",
				Auth: config.AuthConfig{
					Enabled: false,
				},
				Routes: []config.RouteConfig{
					{Path: "/public/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
		},
	}

	reg, err := registry.New(cfg)
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}

	gwSrv, err := server.New(cfg, reg)
	if err != nil {
		t.Fatalf("failed to initialize gateway server: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Helper to generate JWT tokens signed with our RSA private key
	generateToken := func(sub string, expiration time.Time, key *rsa.PrivateKey) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"sub": sub,
			"exp": expiration.Unix(),
			"nbf": time.Now().Add(-1 * time.Minute).Unix(),
		})
		tokenString, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("failed to sign token: %v", err)
		}
		return tokenString
	}

	t.Run("public route does not require authentication", func(t *testing.T) {
		lastReceivedPath = ""
		lastReceivedConsumer = ""

		resp, err := client.Get(gateway.URL + "/public/hello")
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		if lastReceivedPath != "/hello" {
			t.Errorf("expected forwarded path '/hello', got '%s'", lastReceivedPath)
		}

		if lastReceivedConsumer != "" {
			t.Errorf("expected no X-Consumer header forwarded, got '%s'", lastReceivedConsumer)
		}
	})

	t.Run("secure route rejects requests with missing API key", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/secure/data")
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected status 401 Unauthorized, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !bytesContain(body, "Missing API Key") {
			t.Errorf("expected error message for missing API key, got '%s'", string(body))
		}
	})

	t.Run("secure route rejects requests with invalid API key", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/secure/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("X-API-Key", "invalid-key-here")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected status 401 Unauthorized, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !bytesContain(body, "Invalid API Key") {
			t.Errorf("expected error message for invalid API key, got '%s'", string(body))
		}
	})

	t.Run("secure route rejects valid key that is not authorized for the service", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/secure/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("X-API-Key", "bob-secret-key") // Bob is valid globally, but not on secure-service

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected status 403 Forbidden, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !bytesContain(body, "Insufficient permissions") {
			t.Errorf("expected error message for insufficient permissions, got '%s'", string(body))
		}
	})

	t.Run("secure route accepts valid key and authorized consumer, injecting identification header", func(t *testing.T) {
		lastReceivedPath = ""
		lastReceivedConsumer = ""

		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/secure/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("X-API-Key", "alice-secret-key") // Alice is valid and allowed

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200 OK, got %d", resp.StatusCode)
		}

		if lastReceivedPath != "/data" {
			t.Errorf("expected forwarded path '/data', got '%s'", lastReceivedPath)
		}

		if lastReceivedConsumer != "alice" {
			t.Errorf("expected forwarded X-Consumer header 'alice', got '%s'", lastReceivedConsumer)
		}
	})

	// JWT Test Cases
	t.Run("jwt route rejects requests with missing Auth header", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/jwt/data")
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected status 401 Unauthorized, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !bytesContain(body, "Missing Authorization Header") {
			t.Errorf("expected missing header message, got '%s'", string(body))
		}
	})

	t.Run("jwt route rejects requests with malformed Bearer prefix", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/jwt/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("Authorization", "Token some-token-string")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected status 401 Unauthorized, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !bytesContain(body, "must start with Bearer") {
			t.Errorf("expected malformed bearer message, got '%s'", string(body))
		}
	})

	t.Run("jwt route rejects expired tokens", func(t *testing.T) {
		expiredToken := generateToken("alice", time.Now().Add(-1*time.Hour), privateKey)

		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/jwt/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+expiredToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected status 401 Unauthorized, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !bytesContain(body, "token is expired") {
			t.Errorf("expected expired token message, got '%s'", string(body))
		}
	})

	t.Run("jwt route rejects tokens signed by an untrusted key", func(t *testing.T) {
		untrustedKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		untrustedToken := generateToken("alice", time.Now().Add(1*time.Hour), untrustedKey)

		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/jwt/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+untrustedToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", resp.StatusCode)
		}
	})

	t.Run("jwt route rejects valid token belonging to an unauthorized consumer", func(t *testing.T) {
		tokenForBob := generateToken("bob", time.Now().Add(1*time.Hour), privateKey) // Bob is not allowed on jwt-service

		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/jwt/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenForBob)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected status 403 Forbidden, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !bytesContain(body, "Insufficient permissions") {
			t.Errorf("expected forbidden message, got '%s'", string(body))
		}
	})

	t.Run("jwt route accepts valid token and authorized consumer, injecting consumer identifier", func(t *testing.T) {
		lastReceivedPath = ""
		lastReceivedConsumer = ""

		tokenForAlice := generateToken("alice", time.Now().Add(1*time.Hour), privateKey)

		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/jwt/data", nil)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenForAlice)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200 OK, got %d", resp.StatusCode)
		}

		if lastReceivedPath != "/data" {
			t.Errorf("expected path '/data', got '%s'", lastReceivedPath)
		}

		if lastReceivedConsumer != "alice" {
			t.Errorf("expected forwarded consumer 'alice', got '%s'", lastReceivedConsumer)
		}
	})
}

// bytesContain returns true if sub is found in data.
func bytesContain(data []byte, sub string) bool {
	return ioContains(string(data), sub)
}

func ioContains(str, sub string) bool {
	// A basic string contains check
	for i := 0; i <= len(str)-len(sub); i++ {
		if str[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
