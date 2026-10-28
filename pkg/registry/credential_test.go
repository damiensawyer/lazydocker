package registry

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCredentialRedaction(t *testing.T) {
	c := Credential{
		Username:      "alice",
		Password:      "hunter2-secret",
		IdentityToken: "id-secret",
		RegistryToken: "reg-secret",
	}

	// Every rendering verb must produce exactly the redacted form.
	const wantFull = "registry.Credential{Username:alice, Password:set, IdentityToken:set, RegistryToken:set}"
	for name, rendered := range map[string]string{
		"%v":  fmt.Sprintf("%v", c),
		"%+v": fmt.Sprintf("%+v", c),
		"%s":  fmt.Sprintf("%s", c),
		"%#v": fmt.Sprintf("%#v", c),
	} {
		if rendered != wantFull {
			t.Errorf("%s rendering = %q, want %q", name, rendered, wantFull)
		}
	}

	const wantEmpty = "registry.Credential{Username:bob, Password:unset, IdentityToken:unset, RegistryToken:unset}"
	if got := fmt.Sprintf("%v", Credential{Username: "bob"}); got != wantEmpty {
		t.Errorf("partial credential rendering = %q, want %q", got, wantEmpty)
	}

	// Map and slice ELEMENTS do go through Stringer/GoStringer.
	if s := fmt.Sprintf("%v", map[string]Credential{"k": c}); strings.Contains(s, "hunter2-secret") {
		t.Errorf("map rendering leaked: %s", s)
	}
	// NOTE: fmt does not consult Stringer/GoStringer for struct FIELDS, so a
	// Credential embedded in another printed struct renders raw — a fmt
	// limitation every Go type shares (time.Duration behaves the same); the
	// Credential doc comment forbids embedding it in printable structs.
}

func TestCredentialMarshalJSON(t *testing.T) {
	c := Credential{Username: "alice", Password: "p", IdentityToken: "i"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, secret := range []string{`"p"`, `"i"`, ":p,", `"IdentityToken":"i"`} {
		if strings.Contains(s, secret) {
			t.Errorf("json leaked %q: %s", secret, s)
		}
	}
	for _, want := range []string{`"Username":"alice"`, `"PasswordSet":true`, `"IdentityTokenSet":true`, `"RegistryTokenSet":false`} {
		if !strings.Contains(s, want) {
			t.Errorf("json missing %s: %s", want, s)
		}
	}
}

func TestCredentialHelpers(t *testing.T) {
	var nilCred *Credential
	if nilCred.hasBasicAuth() || nilCred.hasIdentityToken() || nilCred.hasRegistryToken() {
		t.Error("nil credential must report no material")
	}
	c := &Credential{Username: "u"}
	if c.hasBasicAuth() {
		t.Error("username without password is not basic material")
	}
	if got := (&Credential{}).String(); !strings.Contains(got, "unset") {
		t.Errorf("zero credential rendering: %s", got)
	}
}
