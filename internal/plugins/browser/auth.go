package browser

import (
	"fmt"
	"os"
	"strings"
)

// validTokenEnv validates a portable environment variable name, not its value.
func validTokenEnv(env string) error {
	for i := 0; i < len(env); i++ {
		c := env[i]
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return fmt.Errorf("invalid browser token environment variable name %q", env)
	}
	return nil
}

// browserToken resolves authentication at runtime, without caching the secret.
func browserToken(env string) (string, error) {
	if err := validTokenEnv(env); err != nil {
		return "", err
	}
	if env == "" {
		return "", nil
	}
	token, ok := os.LookupEnv(env)
	if !ok || token == "" {
		return "", fmt.Errorf("browser token environment variable %q is missing or empty", env)
	}
	if strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("browser token environment variable %q contains CR or LF", env)
	}
	return token, nil
}
