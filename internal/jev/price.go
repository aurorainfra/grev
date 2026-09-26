package jev

import (
	"os"
	"strconv"
	"strings"
)

// DefaultModel is pinned rather than "jev-latest": thresholds are tuned per
// model version, and an alias can move under us.
const DefaultModel = "jev-1.13.0"

// Model returns the model to use: flag, then TYPESAFE_DEFAULT_MODEL, then DefaultModel.
func Model(flag string) string {
	if flag != "" {
		return flag
	}
	if m := os.Getenv("TYPESAFE_DEFAULT_MODEL"); m != "" {
		return m
	}
	return DefaultModel
}

// Price is USD per million tokens.
type Price struct {
	In, Out float64
}

// prices by model id prefix; longest prefix wins. Aliases resolve to the
// versioned id reported in responses, which is what we bill against.
var prices = map[string]Price{
	"jev-1.13":    {In: 0.042},
	"jev-1.12":    {In: 0.042},
	"jev-latest":  {In: 0.042},
	"jev-preview": {In: 0.042},
}

// SetPrice sets the price of a model id (or id prefix), e.g. from a
// [model "…"] config section. Call it before any request is made.
func SetPrice(model string, p Price) { prices[model] = p }

// modelIDs lists the ids to try when pricing a model: the id as reported,
// then the part after any provider prefix, so a gateway reporting
// "typesafe/jev-1.13-20260917" still finds the "jev-1.13" entry.
func modelIDs(model string) []string {
	if i := strings.LastIndex(model, "/"); i >= 0 && i+1 < len(model) {
		if tail := model[i+1:]; tail != model {
			return []string{model, tail}
		}
	}
	return []string{model}
}

// PriceOf returns the price of model; GREV_PRICE_PER_MTOK overrides the input
// price. ok is false for unknown models.
func PriceOf(model string) (p Price, ok bool) {
	if v := os.Getenv("GREV_PRICE_PER_MTOK"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return Price{In: f}, true
		}
	}
	for _, id := range modelIDs(model) {
		if p, ok = priceOf(id); ok {
			return p, true
		}
	}
	return Price{}, false
}

// priceOf matches one id against the table by longest prefix.
func priceOf(model string) (p Price, ok bool) {
	best := -1
	for prefix, pr := range prices {
		if strings.HasPrefix(model, prefix) && len(prefix) > best {
			p, best = pr, len(prefix)
		}
	}
	return p, best >= 0
}

// Cost is the USD cost of a request's usage. A cost reported by the API wins,
// since it is what was actually charged; otherwise it is computed from the
// price table for model. GREV_PRICE_PER_MTOK, being an explicit override,
// beats the reported cost.
func Cost(model string, u Usage) (float64, bool) {
	if u.Cost != nil && os.Getenv("GREV_PRICE_PER_MTOK") == "" {
		return *u.Cost, true
	}
	p, ok := PriceOf(model)
	return (float64(u.InputTokens)*p.In + float64(u.OutputTokens)*p.Out) / 1e6, ok
}
