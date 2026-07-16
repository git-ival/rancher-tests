package jenkins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateAuthWithEncodedToken(t *testing.T) {
	const user = "jenkins-user"
	const token = "jenkins-token"
	encodedToken := base64.StdEncoding.EncodeToString([]byte(token))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotUser, gotToken, ok := r.BasicAuth()
		if !ok || gotUser != user || gotToken != token {
			t.Errorf("BasicAuth = (%q, %q, %v)", gotUser, gotToken, ok)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(whoAmIResponse{Authenticated: true, Name: user})
	}))
	defer server.Close()

	name, err := NewClient(server.URL, user, encodedToken).ValidateAuth(context.Background())
	if err != nil {
		t.Fatalf("ValidateAuth: %v", err)
	}
	if name != user {
		t.Fatalf("name = %q, want %q", name, user)
	}
}

func TestValidateAuthWithEncodedBasicCredential(t *testing.T) {
	const user = "jenkins-user"
	const token = "jenkins-token"
	encodedCredential := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crumbIssuer/api/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Basic "+encodedCredential {
			t.Errorf("Authorization = %q", got)
		}
		gotUser, gotToken, ok := r.BasicAuth()
		if !ok || gotUser != user || gotToken != token {
			t.Errorf("BasicAuth = (%q, %q, %v)", gotUser, gotToken, ok)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(whoAmIResponse{Authenticated: true, Name: user})
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, "ignored", " Basic "+encodedCredential+"\n").ValidateAuth(context.Background()); err != nil {
		t.Fatalf("ValidateAuth: %v", err)
	}
}
