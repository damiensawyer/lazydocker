package registrytest

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServerAnonymous(t *testing.T) {
	s := New(Config{})
	defer s.Close()

	if !strings.HasPrefix(s.URL(), "http://") {
		t.Errorf("URL = %q, want plain http", s.URL())
	}

	resp, err := http.Get(s.URL() + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
		t.Errorf("api version header = %q, want registry/2.0", got)
	}
}

func TestServerBasicAuth(t *testing.T) {
	s := New(Config{Auth: AuthBasic, Username: "alice", Password: "wonderland"})
	defer s.Close()

	t.Run("challenge without credentials", func(t *testing.T) {
		resp, err := http.Get(s.URL() + "/v2/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if !strings.HasPrefix(resp.Header.Get("Www-Authenticate"), "Basic ") {
			t.Errorf("challenge = %q, want Basic", resp.Header.Get("Www-Authenticate"))
		}
	})

	t.Run("accepted with credentials", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/", nil)
		req.SetBasicAuth("alice", "wonderland")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})
}

func TestServerBearerAuth(t *testing.T) {
	s := New(Config{Auth: AuthBearer, Username: "alice", Password: "wonderland"})
	defer s.Close()

	t.Run("challenge names realm and service", func(t *testing.T) {
		resp, err := http.Get(s.URL() + "/v2/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		challenge := resp.Header.Get("Www-Authenticate")
		if !strings.HasPrefix(challenge, "Bearer ") {
			t.Fatalf("challenge = %q, want Bearer", challenge)
		}
		for _, want := range []string{`realm="` + s.RealmURL() + `"`, `service="` + s.Host() + `"`} {
			if !strings.Contains(challenge, want) {
				t.Errorf("challenge %q missing %s", challenge, want)
			}
		}
	})

	t.Run("anonymous token flow", func(t *testing.T) {
		resp, err := http.Get(s.RealmURL() + "?service=" + s.Host())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("token status = %d, want 200", resp.StatusCode)
		}
		var body struct {
			Token     string `json:"token"`
			ExpiresIn int    `json:"expires_in"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Token == "" || body.ExpiresIn != 300 {
			t.Errorf("token response = %+v, want a token with expires_in 300", body)
		}

		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/", nil)
		req.Header.Set("Authorization", "Bearer "+body.Token)
		authed, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer authed.Body.Close()
		if authed.StatusCode != http.StatusOK {
			t.Errorf("status with issued token = %d, want 200", authed.StatusCode)
		}
	})

	t.Run("invalid basic auth at the realm is rejected", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, s.RealmURL(), nil)
		req.SetBasicAuth("alice", "nope")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("unknown token is rejected at /v2/", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/", nil)
		req.Header.Set("Authorization", "Bearer made-up-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})
}

func TestServerUnknownEndpointOCIError(t *testing.T) {
	s := New(Config{})
	defer s.Close()

	resp, err := http.Get(s.URL() + "/v2/_catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Errors) != 1 || body.Errors[0].Code != "NAME_UNKNOWN" {
		t.Errorf("oci error body = %+v, want one NAME_UNKNOWN", body)
	}
}

func TestServerRecording(t *testing.T) {
	s := New(Config{Auth: AuthBasic, Username: "alice", Password: "wonderland"})
	defer s.Close()

	req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/?x=1", nil)
	req.SetBasicAuth("alice", "wonderland")
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}

	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodGet || r.Path != "/v2/" || r.RawQuery != "x=1" {
		t.Errorf("recorded = %+v", r)
	}
	if r.Authorization != "basic" || r.BasicUsername != "alice" {
		t.Errorf("recorded auth = %q/%q, want basic/alice", r.Authorization, r.BasicUsername)
	}

	// The recording must never contain the password.
	if strings.Contains(fmt.Sprint(reqs), "wonderland") {
		t.Error("recording leaked the password")
	}
}

func TestServerTokenCounters(t *testing.T) {
	s := New(Config{Auth: AuthBearer, TokenTTL: 60 * time.Second})
	defer s.Close()

	if got := s.TokensIssued(); got != 0 {
		t.Fatalf("tokens issued = %d, want 0", got)
	}
	for i := 0; i < 2; i++ {
		if _, err := http.Get(s.RealmURL()); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.TokensIssued(); got != 2 {
		t.Errorf("tokens issued = %d, want 2", got)
	}
	if got := len(s.TokenRequests()); got != 2 {
		t.Errorf("token requests = %d, want 2", got)
	}
}

func TestServerRealmOverride(t *testing.T) {
	s := New(Config{Auth: AuthBearer})
	defer s.Close()

	s.SetRealmBaseURL("https://auth.example.com")
	if got := s.RealmURL(); got != "https://auth.example.com/token" {
		t.Errorf("realm = %q, want https://auth.example.com/token", got)
	}

	s.SetRealmBaseURL("https://auth.example.com/")
	if got := s.RealmURL(); got != "https://auth.example.com/token" {
		t.Errorf("realm = %q (trailing slash must be trimmed), want https://auth.example.com/token", got)
	}
}

func TestServerTLS(t *testing.T) {
	// A config with no certificate: httptest appends its own, so the server
	// still comes up on https and rejects plain http.
	s := New(Config{TLS: &tls.Config{}})
	defer s.Close()

	if !strings.HasPrefix(s.URL(), "https://") {
		t.Errorf("URL = %q, want https", s.URL())
	}

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Get(s.URL() + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServerConcurrentUse(t *testing.T) {
	s := New(Config{Auth: AuthBearer, Username: "alice", Password: "wonderland"})
	defer s.Close()

	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			resp, err := http.Get(s.RealmURL())
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d", resp.StatusCode)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if got := s.TokensIssued(); got != 4 {
		t.Errorf("tokens issued = %d, want 4", got)
	}
}
