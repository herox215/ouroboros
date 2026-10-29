package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"ouroboros/internal/llm"
)

// currency and amount are what a balance of DeepSeek is shown with: a code
// like "USD" and a decimal like "12.34". Anything else is not shown.
var (
	currency = regexp.MustCompile(`^[A-Z]{3}$`)
	amount   = regexp.MustCompile(`^-?[0-9]{1,15}(\.[0-9]{1,8})?$`)
)

// balance reads the balance of the account of key: GET /user/balance at
// endpoint. Neither the answer nor an error of the transport is told as it
// is, as either may hold the key.
func balance(ctx context.Context, endpoint, key string) (llm.Usage, error) {
	body, err := llm.FetchUsage(ctx, "DeepSeek balance", strings.TrimRight(endpoint, "/")+"/user/balance", key)
	if err != nil {
		return llm.Usage{}, err
	}
	return parseBalance(body)
}

// parseBalance reads what /user/balance answers, like
//
//	{"is_available": true, "balance_infos": [{"currency": "USD", "total_balance": "12.34", …}]}
//
// with the total of every currency the account holds credit in, and whether
// the balance still allows calls. A field that is missing or not as
// documented fails the whole answer rather than show a balance that may be
// wrong.
func parseBalance(body []byte) (llm.Usage, error) {
	var response struct {
		IsAvailable *bool `json:"is_available"`
		Balances    *[]struct {
			Currency string `json:"currency"`
			Total    string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	invalid := errors.New("invalid DeepSeek balance response")
	if err := json.Unmarshal(body, &response); err != nil || response.IsAvailable == nil || response.Balances == nil {
		return llm.Usage{}, invalid
	}
	usage := llm.Usage{Insufficient: !*response.IsAvailable}
	for _, b := range *response.Balances {
		if !currency.MatchString(b.Currency) || !amount.MatchString(b.Total) {
			return llm.Usage{}, invalid
		}
		usage.Balances = append(usage.Balances, llm.UsageBalance{Currency: b.Currency, Amount: b.Total})
	}
	return usage, nil
}
