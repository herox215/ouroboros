package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 5 * time.Minute}

// Send delivers text to the agent at baseURL and returns its reply.
// Messages with the same contextID belong to the same conversation.
func Send(ctx context.Context, baseURL, contextID, text string) (string, error) {
	req := request{JSONRPC: "2.0", ID: json.RawMessage(`"` + NewID() + `"`), Method: MethodSend}
	req.Params.Message = NewMessage("user", contextID, text)
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("a2a: HTTP %d", resp.StatusCode)
	}

	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("a2a: invalid response: %w", err)
	}
	switch {
	case res.Error != nil:
		return "", res.Error
	case res.Result == nil:
		return "", errors.New("a2a: response has no message")
	}
	return res.Result.Text(), nil
}
