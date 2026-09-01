package client

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// TokenPrefix mirrors the shape of a Claude Code OAuth token. The agent SDK
// only sends a credential as `Authorization: Bearer` with the oauth beta
// header when it looks like an oat, so issued tokens must keep this prefix.
const TokenPrefix = "sk-ant-oat01-"

// IssueToken mints a new client token and returns it with its digest. The raw
// token is shown once at issue time; only the digest belongs in clients.yaml.
func IssueToken() (token, digest string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// IssueAndDescribe mints a token for name and writes the operator instructions:
// the raw token (shown only here), the clients.yaml entry, and how the client
// should present it. The name is validated before anything is printed so a
// typo cannot produce an entry the registry would later reject.
func IssueAndDescribe(name string, out io.Writer) error {
	token, digest, err := IssueToken()
	if err != nil {
		return err
	}
	reg := Registry{Clients: []Client{{Name: name, TokenSHA256: digest}}}
	if err := reg.Validate(); err != nil {
		return err
	}

	fmt.Fprintf(out, "Token for %s (shown once, store it now):\n\n  %s\n\n", name, token)
	fmt.Fprintf(out, "Add to clients.yaml:\n\n  - name: %s\n    tokenSHA256: %s\n\n", name, digest)
	fmt.Fprintf(out, "The client uses it as:\n\n  export CLAUDE_CODE_OAUTH_TOKEN=%s\n  export ANTHROPIC_BASE_URL=http://<proxy-host>:8080\n", token)
	return nil
}
