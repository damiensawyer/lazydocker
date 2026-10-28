package registry

import "encoding/json"

// Credential holds the material used to authenticate against a single
// registry. Values must never be logged, rendered, or included in error
// strings; String, GoString and MarshalJSON all redact the secret fields so a
// stray fmt.Printf("%v") or a JSON dump of the credential itself cannot leak
// them.
//
// Caveat inherited from fmt: Stringer/GoStringer are not consulted for
// struct *fields*, only for top-level operands (and container elements), so
// never embed a Credential in a struct you print wholesale — print the
// credential itself.
type Credential struct {
	// Username and Password are sent as HTTP basic auth, either directly to
	// the registry (on a Basic challenge) or to the registry's token realm
	// (on a Bearer challenge), exactly like `docker login` credentials.
	Username string
	Password string

	// IdentityToken is an OAuth-style refresh token (e.g. an identity token
	// from a credential helper). It is exchanged at the token realm using
	// "Authorization: Bearer <token>" instead of basic auth.
	IdentityToken string

	// RegistryToken is a ready-to-use bearer token for the registry itself.
	// When set it is attached to every registry request and the token realm
	// is never contacted.
	RegistryToken string
}

func redactValue(v string) string {
	if v == "" {
		return "unset"
	}
	return "set"
}

// String implements fmt.Stringer with every secret redacted. The username is
// shown (as `docker login` and the UI do); it is not treated as a secret.
func (c Credential) String() string {
	return "registry.Credential{Username:" + c.Username +
		", Password:" + redactValue(c.Password) +
		", IdentityToken:" + redactValue(c.IdentityToken) +
		", RegistryToken:" + redactValue(c.RegistryToken) + "}"
}

// GoString implements fmt.GoStringer (used by %#v) with every secret redacted.
func (c Credential) GoString() string {
	return c.String()
}

// MarshalJSON emits only the username plus which secret fields are set, never
// their values, so JSON debug dumps stay safe.
func (c Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Username         string `json:"Username"`
		PasswordSet      bool   `json:"PasswordSet"`
		IdentityTokenSet bool   `json:"IdentityTokenSet"`
		RegistryTokenSet bool   `json:"RegistryTokenSet"`
	}{
		Username:         c.Username,
		PasswordSet:      c.Password != "",
		IdentityTokenSet: c.IdentityToken != "",
		RegistryTokenSet: c.RegistryToken != "",
	})
}

// hasBasicAuth reports whether basic-auth material is present.
func (c *Credential) hasBasicAuth() bool {
	return c != nil && c.Username != "" && c.Password != ""
}

// hasIdentityToken reports whether an identity token is present.
func (c *Credential) hasIdentityToken() bool {
	return c != nil && c.IdentityToken != ""
}

// hasRegistryToken reports whether a ready registry bearer token is present.
func (c *Credential) hasRegistryToken() bool {
	return c != nil && c.RegistryToken != ""
}
