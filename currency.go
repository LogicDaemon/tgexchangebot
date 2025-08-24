package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Normalized currency codes we're supporting now
const (
	CurRUR = "RUR"
	CurUSD = "USD"
	CurGEL = "GEL"
)

// Single source of truth: currency specifications
type currencySpec struct {
	Code    string   // normalized code, e.g., RUR
	Symbol  string   // display symbol, e.g., ₽
	Aliases []string // lowercase aliases including symbols and words
}

var currencySpecs = []currencySpec{
	{Code: CurRUR, Symbol: "₽", Aliases: []string{"р", "₽", "r", "rub", "rur"}},
	{Code: CurUSD, Symbol: "$", Aliases: []string{"$", "usd", "долл"}},
	{Code: CurGEL, Symbol: "₾", Aliases: []string{"л", "₾", "ლ", "лар", "лари", "gel"}},
}

// Regexp rules mapping to representation index
type currencyRegexpRule struct {
	re    *regexp.Regexp
	index int
}

var currencyRegexpRules []currencyRegexpRule

type regexSpec struct {
	Pattern string
	Code    string // normalized currency code
}

var rawRegexSpecs = []regexSpec{
	{Pattern: `^р.*`, Code: CurRUR},
	{Pattern: `^л.*`, Code: CurGEL},
	{Pattern: `^д.*`, Code: CurUSD},
}

// Shared HTTP client for TBC API calls; per-request timeouts via context
var tbcHTTPClient *http.Client

// Derived at init
var (
	currencyCodes           []string
	currencyRepresentations []string
	currencyIndexByCode     map[string]int
	// Alias maps a lowercase alias to the index into currency arrays
	currencyAliasToIndex map[string]int
)

func init() {
	tbcHTTPClient = &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			IdleConnTimeout:       5 * time.Minute,
		},
	}
	initCurrencyMappings()
}

func initCurrencyMappings() {
	// Build code arrays and maps
	currencyCodes = make([]string, 0, len(currencySpecs))
	currencyRepresentations = make([]string, 0, len(currencySpecs))
	currencyIndexByCode = make(map[string]int, len(currencySpecs))
	currencyAliasToIndex = make(map[string]int)
	for i, s := range currencySpecs {
		currencyCodes = append(currencyCodes, s.Code)
		currencyRepresentations = append(currencyRepresentations, s.Symbol)
		currencyIndexByCode[strings.ToUpper(s.Code)] = i
		for _, a := range s.Aliases {
			currencyAliasToIndex[strings.ToLower(a)] = i
		}
	}

	// Compile regex rules referencing codes
	currencyRegexpRules = make([]currencyRegexpRule, 0, len(rawRegexSpecs))
	for _, rr := range rawRegexSpecs {
		idx, ok := currencyIndexByCode[strings.ToUpper(rr.Code)]
		if !ok {
			continue // skip unknown code
		}
		currencyRegexpRules = append(currencyRegexpRules, currencyRegexpRule{
			re:    regexp.MustCompile(rr.Pattern),
			index: idx,
		})
	}

	// Shared HTTP client for TBC API calls; per-request timeouts via context
}

// normalizeCurrency tries to turn an input token into a normalized currency code and its representation
// Returns normalized (like RUR) and display representation
func normalizeCurrency(token string) (normalized string, ok bool) {
	t := strings.ToLower(strings.TrimSpace(token))

	if idx, found := currencyAliasToIndex[t]; found {
		return currencyCodes[idx], true
	}
	for _, rule := range currencyRegexpRules {
		if rule.re.MatchString(t) {
			return currencyCodes[rule.index], true
		}
	}

	// Also allow direct normalized codes (case-insensitive) like USD, RUR, GEL
	upper := strings.ToUpper(t)
	if _, ok := currencyIndexByCode[upper]; ok {
		return upper, true
	}

	return "", false
}

// formatCodeWithRep returns "CODE (REP)" if a distinct representation exists, otherwise just CODE
func formatCodeWithRep(code string) string {
	idx, ok := currencyIndexByCode[strings.ToUpper(code)]
	if !ok {
		return code
	}
	return fmt.Sprintf("%s (%s)", currencyRepresentations[idx], code)
}

// optionsForError returns possible options the user can use
func optionsForError() string {
	// Compose list of aliases and regex hints
	// unique alias keys grouped by normalized code
	keysByIndex := map[int][]string{}
	for alias, idx := range currencyAliasToIndex {
		keysByIndex[idx] = append(keysByIndex[idx], alias)
	}
	// Order by representation order
	var parts []string
	for idx, rep := range currencyRepresentations {
		aliases := keysByIndex[idx]
		sort.Strings(aliases)
		if len(aliases) > 0 {
			parts = append(parts, fmt.Sprintf("%s: %s", rep, strings.Join(aliases, ", ")))
		} else {
			parts = append(parts, rep)
		}
	}
	return "Supported currencies and aliases: " + strings.Join(parts, " | ") + "; regex: р.* => RUR"
}

// TBC Bank API structures
type tbcCommercialRate struct {
	Currency string  `json:"currency"`
	Buy      float64 `json:"buy"`
	Sell     float64 `json:"sell"`
}

type tbcCommercialRatesResponse struct {
	Base                string              `json:"base"`
	CommercialRatesList []tbcCommercialRate `json:"commercialRatesList"`
}

// getTBCCurrencyRatesCtx fetches commercial exchange rates from TBC Bank API with context
// base URL: https://test-api.tbcbank.ge/v1/exchange-rates/commercial/
func getTBCCurrencyRatesCtx(ctx context.Context, apiKey string) (*tbcCommercialRatesResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://test-api.tbcbank.ge/v1/exchange-rates/commercial/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", apiKey)
	resp, err := tbcHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("TBC rates request failed with status %s: %s",
			resp.Status, resp.Body)
	}
	var out tbcCommercialRatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Conversion and caching ---

type tbcRate struct {
	Buy         float64 // GEL per unit of foreign currency when bank buys foreign (you sell foreign)
	Sell        float64 // GEL per unit when bank sells foreign (you buy foreign)
	LastUpdated time.Time
}

type tbcRateCache struct {
	apiKey      string
	rates       map[string]tbcRate // keyed by currency code, e.g., USD, RUR
	lastUpdated time.Time
	reqCh       chan interface{}
	base        string // base currency reported by API; e.g., GEL
}

// initCurrencyRates creates and starts the rate cache manager
func initCurrencyRates(apiKey string) *tbcRateCache {
	if strings.TrimSpace(apiKey) == "" {
		log.Println("Missing TBC API key. Please create a developer account and obtain an API key: https://developers.tbcbank.ge/docs/create-developer-account")
		return nil
	}
	c := &tbcRateCache{apiKey: apiKey,
		rates: make(map[string]tbcRate),
		reqCh: make(chan interface{}, 32),
	}
	go c.run()
	return c
}

// request/response messages for the manager loop
type computeReq struct {
	knownCurrency string
	knownAmount   float64
	offerType     OfferType
	respCh        chan computeResp
}

type computeResp struct {
	otherCurrency string
	otherAmount   float64
	err           error
}

type refreshReq struct {
	timeout time.Duration
	respCh  chan error
}

// apply messages (produced by background fetchers)
type applySingle struct {
	code string
	rate tbcRate
}

// snapshot request/response for safe read access
type snapshotReq struct {
	respCh chan snapshotResp
}

type snapshotResp struct {
	base        string
	cacheUpdate time.Time
	rates       map[string]tbcRate
}

// run is the manager goroutine processing requests and updates
func (c *tbcRateCache) run() {
	// Initial refresh best-effort with 60s timeout
	_ = c._refresh(60 * time.Second)
	ticker := time.NewTicker(4 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case msg := <-c.reqCh:
			switch m := msg.(type) {
			case computeReq:
				otherCur, otherAmt, err := c._computeCounterAmountInternal(m.knownCurrency, m.knownAmount, m.offerType)
				m.respCh <- computeResp{otherCurrency: otherCur, otherAmount: otherAmt, err: err}
			case refreshReq:
				m.respCh <- c._refresh(m.timeout)
			case applySingle:
				r := m.rate
				if r.LastUpdated.IsZero() {
					r.LastUpdated = time.Now()
				}
				c.rates[m.code] = r
				c.lastUpdated = time.Now()
			case snapshotReq:
				// produce a deep copy of the rates map for safe use outside
				copyMap := make(map[string]tbcRate, len(c.rates))
				for k, v := range c.rates {
					copyMap[k] = v
				}
				m.respCh <- snapshotResp{base: c.base, cacheUpdate: c.lastUpdated, rates: copyMap}
			}
		case <-ticker.C:
			c.refreshIfStaleAsync()
		}
	}
}

// snapshot returns a safe copy of current base, cache timestamp, and rates
func (c *tbcRateCache) snapshot() (string, time.Time, map[string]tbcRate) {
	respCh := make(chan snapshotResp, 1)
	c.reqCh <- snapshotReq{respCh: respCh}
	resp := <-respCh
	return resp.base, resp.cacheUpdate, resp.rates
}

// _refresh updates the entire rates map from the full list endpoint
// since it accesses the map, it must be called from the manager goroutine only.
func (c *tbcRateCache) _refresh(timeout time.Duration) error {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout <= 0 {
		ctx = context.Background()
		cancel = func() {}
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	defer cancel()
	out, err := getTBCCurrencyRatesCtx(ctx, c.apiKey)
	if err != nil {
		return err
	}
	// Rebuild map
	newMap := make(map[string]tbcRate, len(out.CommercialRatesList))
	for _, r := range out.CommercialRatesList {
		code := strings.ToUpper(strings.TrimSpace(r.Currency))
		if code == "" {
			continue
		}
		newMap[code] = tbcRate{Buy: r.Buy, Sell: r.Sell}
	}
	if c.base != "" && !strings.EqualFold(c.base, out.Base) {
		panic(fmt.Errorf("TBC base currency changed from %s to %s", c.base, out.Base))
	}
	c.base = strings.ToUpper(out.Base)
	// set uniform timestamps
	now := time.Now()
	for k, v := range newMap {
		v.LastUpdated = now
		newMap[k] = v
	}
	c.rates = newMap
	c.lastUpdated = now
	return nil
}

// refreshIfStaleAsync kicks off a background full refresh if any per-currency rate is older than the threshold.
// Does not access the existing cache map, replaces the entire map when done.
func (c *tbcRateCache) refreshIfStaleAsync() {
	updateThreshold := 2 * time.Hour
	// Determine if any per-currency rate is older than 2h; if not, align cache timestamp to oldest and skip
	if !c.lastUpdated.IsZero() && time.Since(c.lastUpdated) > updateThreshold {
		return
	}
	if len(c.rates) > 0 {
		oldest := time.Now()
		anyOld := false
		for _, v := range c.rates {
			if v.LastUpdated.IsZero() {
				anyOld = true
				break
			}
			if v.LastUpdated.Before(oldest) {
				oldest = v.LastUpdated
			}
		}
		if !anyOld {
			if time.Since(oldest) < updateThreshold {
				// Keep cache timestamp aligned to the oldest per-currency update
				c.lastUpdated = oldest
				return
			}
		}
	}
	// fetch in background and apply when done
	apiKey := c.apiKey
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := getTBCCurrencyRatesCtx(ctx, apiKey)
		if err != nil {
			return
		}
		if !strings.EqualFold(c.base, out.Base) {
			log.Printf("TBC base currency changed from %s to %s", c.base, out.Base)
			c.base = strings.ToUpper(out.Base)
		}
		m := make(map[string]tbcRate, len(out.CommercialRatesList))
		now := time.Now()
		for _, r := range out.CommercialRatesList {
			curr := strings.ToUpper(r.Currency)
			if _, ok := currencyIndexByCode[curr]; !ok {
				continue // skip unknown currency
			}
			m[curr] = tbcRate{Buy: r.Buy, Sell: r.Sell, LastUpdated: now}
		}
		c.lastUpdated = now
		c.rates = m

	}()
}

// _startSingleRateUpdate starts a 60s background fetch for a single currency and applies it when finished.
// since it accesses the map, it must be called from the manager goroutine only.
func (c *tbcRateCache) _startSingleRateUpdate(code string) {
	cur := strings.ToUpper(strings.TrimSpace(code))
	// Skip base currency: its rate to base is 1 by definition
	if cur == "" || strings.EqualFold(cur, c.base) {
		return
	}
	apiKey := c.apiKey
	ch := c.reqCh
	url := fmt.Sprintf("https://test-api.tbcbank.ge/v1/exchange-rates/commercial?currency=%s", cur)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return
		}
		req.Header.Set("apikey", apiKey)
		resp, err := tbcHTTPClient.Do(req)
		if err != nil {
			log.Printf("Single rate request for %s failed: %v", cur, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("Single rate request for %s status %s: %s", cur, resp.Status, resp.Body)
			return
		}
		var out tbcCommercialRatesResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			log.Printf("Error decoding single rate response %s: %v", resp.Body, err)
			return
		}
		for _, r := range out.CommercialRatesList {
			if strings.EqualFold(r.Currency, cur) {
				ch <- applySingle{code: cur, rate: tbcRate{Buy: r.Buy, Sell: r.Sell, LastUpdated: time.Now()}}
				break
			}
		}
	}()
}

// getPairRateFromList computes conversion using cached list via GEL base and buy/sell logic.
// Returns converted amount and true if computation was possible.
func (c *tbcRateCache) getPairRateFromList(from, to string, amount float64) (float64, bool) {
	if from == to {
		return amount, true
	}

	// foreign A -> foreign B via base
	rateFrom := c.cachedRate(from)
	rateTo := c.cachedRate(to)
	if rateFrom.LastUpdated.IsZero() || time.Since(rateFrom.LastUpdated) > time.Hour {
		go func() {
			
		}()

	if rateFrom.Buy == 0 || rateTo.Sell == 0 {
		return 0, false
	}
	gel := amount * rateFrom.Buy
	return gel / rateTo.Sell, true
}

func (c *tbcRateCache) cachedRate(from string) tbcRate {
	if from == c.base {
		return tbcRate{Buy: 1.0, Sell: 1.0}
	}
	rateFrom, ok := c.rates[from]
	if !ok {
		return tbcRate{}
	}
	return rateFrom
}

// tryConvertEndpoint tries the convert endpoint with 2s timeout; returns value or error
func (c *tbcRateCache) tryConvertEndpoint(from, to string, amount float64) (float64, error) {
	url := fmt.Sprintf("https://test-api.tbcbank.ge/v1/exchange-rates/commercial/convert?From=%s&To=%s&Amount=%s",
		from, to, trimFloat(amount))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("apikey", c.apiKey)
	resp, err := tbcHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("convert endpoint status %s: %s", resp.Status, resp.Body)
	}
	var out struct {
		From   string  `json:"from"`
		To     string  `json:"to"`
		Amount float64 `json:"amount"`
		Value  float64 `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Value, nil
}

// trimFloat formats float without scientific notation to avoid issues in URL
func trimFloat(f float64) string {
	if f == 0 {
		return "0"
	}
	// avoid very long strings
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.10f", f), "0"), ".")
}

// defaultCounterCurrency picks the other side currency when only one side is provided
func defaultCounterCurrency(code string) string {
	up := strings.ToUpper(code)
	switch up {
	case CurUSD:
		return CurRUR
	case CurGEL:
		return CurRUR
	// case CurRUR:
	// 	return CurUSD
	default:
		return CurUSD
	}
}

// public method: compute via manager channel to avoid concurrent map access
func (c *tbcRateCache) computeCounterAmount(knownCurrency string, knownAmount float64, offerType OfferType) (string, float64, error) {
	respCh := make(chan computeResp, 1)
	c.reqCh <- computeReq{knownCurrency: knownCurrency, knownAmount: knownAmount, offerType: offerType, respCh: respCh}
	resp := <-respCh
	return resp.otherCurrency, resp.otherAmount, resp.err
}

// internal compute executed in the manager goroutine
// it calls functions which access the map, so it must be called from the manager goroutine only.
func (c *tbcRateCache) _computeCounterAmountInternal(knownCurrency string, knownAmount float64, offerType OfferType) (otherCurrency string, otherAmount float64, err error) {
	from := knownCurrency
	otherCurrency = defaultCounterCurrency(knownCurrency)
	to := otherCurrency

	updates := make(string, 2)
	for _, cur in []string{from, to} {
		rate, ok := c.rates[cur]
		if !ok || rate.LastUpdated.IsZero() || time.Since(rate.LastUpdated) > time.Hour {
			updates = append(updates, cur)
		}
	}
	if len(updates) == 1 {
		c._startSingleRateUpdate(updates[0])
	} else {
		c.reqCh <- refreshReq{timeout: 5 * time.Second, respCh: make(chan error, 1)
	}

	if v, e := c.tryConvertEndpoint(from, to, knownAmount); e == nil {
		return to, v, nil
	}
	if v, ok := c.getPairRateFromList(from, to, knownAmount); ok {
		return to, v, nil
	}
	return to, 0, errors.New("conversion failed and no cached rate available")
}
