package openai

import (
	"bytes"
	"encoding/json"
	"errors"
)

// GatewayMetadata is an immutable JSON object snapshot. It remains comparable
// with MessageSource and is serialized as an object, not an escaped JSON string.
// Use NewGatewayMetadata to validate and copy gateway-owned context.
type GatewayMetadata string

const maxGatewayMetadata = 16 * 1024

// NewGatewayMetadata takes a bounded, independent snapshot of JSON context.
// Metadata is descriptive, untrusted data; it grants no authority.
func NewGatewayMetadata(fields map[string]any) (GatewayMetadata, error) {
	if len(fields) == 0 {
		return "", nil
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return parseGatewayMetadata(data)
}

func parseGatewayMetadata(data []byte) (GatewayMetadata, error) {
	if len(data) > maxGatewayMetadata {
		return "", errors.New("gateway metadata exceeds 16 KiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return "", nil
	}
	canonical, err := json.Marshal(fields)
	return GatewayMetadata(canonical), err
}

func (m GatewayMetadata) normalized() GatewayMetadata {
	if m == "" {
		return ""
	}
	out, err := parseGatewayMetadata([]byte(m))
	if err != nil {
		return ""
	}
	return out
}

func (m GatewayMetadata) MarshalJSON() ([]byte, error) {
	if m == "" {
		return []byte("null"), nil
	}
	out, err := parseGatewayMetadata([]byte(m))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return []byte("null"), nil
	}
	return []byte(out), nil
}

func (m *GatewayMetadata) UnmarshalJSON(data []byte) error {
	out, err := parseGatewayMetadata(bytes.TrimSpace(data))
	if err == nil {
		*m = out
	}
	return err
}
