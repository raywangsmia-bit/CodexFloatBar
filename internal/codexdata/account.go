package codexdata

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
)

const maxAuthBytes int64 = 1 << 20

type identityClaims struct {
	name  *string
	email *string
}

func readAccount(path string) AccountSummary {
	data, err := readLimitedFile(path, maxAuthBytes)
	if err != nil {
		return AccountSummary{DisplayText: "Codex: not signed in"}
	}
	root, ok := decodeObject(data)
	if !ok {
		return AccountSummary{DisplayText: "Codex: not signed in"}
	}
	authMode := exactString(root, "auth_mode")
	var idToken *string
	if tokens, ok := readObject(root, "tokens"); ok {
		idToken = exactString(tokens, "id_token")
	}
	claims := identityClaims{}
	if idToken != nil {
		claims = decodeIdentityClaims(*idToken)
	}
	authModeValue := ""
	if authMode != nil {
		authModeValue = *authMode
	}
	return AccountSummary{
		AuthMode:    authModeValue,
		DisplayText: formatAccountDisplay(claims, authMode),
	}
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, io.ErrUnexpectedEOF
	}
	return data, nil
}

func decodeIdentityClaims(token string) identityClaims {
	parts := strings.Split(token, ".")
	if len(parts) < 2 || strings.TrimSpace(token) == "" {
		return identityClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return identityClaims{}
	}
	root, ok := decodeObject(payload)
	if !ok {
		return identityClaims{}
	}
	name := firstPresentString(
		exactString(root, "name"),
		exactString(root, "preferred_username"),
		exactString(root, "nickname"),
	)
	return identityClaims{name: name, email: exactString(root, "email")}
}

func exactString(object map[string]json.RawMessage, name string) *string {
	raw, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return &value
}

func firstPresentString(values ...*string) *string {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func formatAccountDisplay(claims identityClaims, authMode *string) string {
	name := ""
	if claims.name != nil {
		name = *claims.name
	}
	email := ""
	if claims.email != nil {
		email = *claims.email
	}
	switch {
	case strings.TrimSpace(name) != "" && strings.TrimSpace(email) != "":
		return "Codex: " + name + " <" + email + ">"
	case strings.TrimSpace(email) != "":
		return "Codex: " + email
	case strings.TrimSpace(name) != "":
		return "Codex: " + name
	case authMode != nil && strings.EqualFold(*authMode, "chatgpt"):
		return "Codex: ChatGPT"
	case authMode != nil:
		return "Codex: " + *authMode
	default:
		return "Codex: not signed in"
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
