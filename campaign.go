package main

import (
	"fmt"
	"math/rand"
	"net/url"
)

type CampaignConfig struct {
	Enabled     bool     `json:"enabled"`
	Probability float64  `json:"probability"`
	Parameters  []string `json:"parameters"`
}

func addCampaigns(plan []WorkItem, c Config) error {
	if !c.Campaign.Enabled {
		return nil
	}
	if c.Campaign.Probability < 0 || c.Campaign.Probability > 1 {
		return fmt.Errorf("campaign probability must be between 0 and 1")
	}
	keys := c.Campaign.Parameters
	if len(keys) == 0 {
		keys = []string{"gclid", "utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"}
	}
	allowed := map[string]bool{"gclid": true, "dclid": true, "gbraid": true, "wbraid": true, "gad_source": true, "gad_campaignid": true, "utm_source": true, "utm_medium": true, "utm_campaign": true, "utm_content": true, "utm_term": true}
	for _, k := range keys {
		if !allowed[k] {
			return fmt.Errorf("unsupported campaign parameter %s", k)
		}
	}
	rng := rand.New(rand.NewSource(c.Seed ^ 0x5ca1ab1e))
	for i := range plan {
		if rng.Float64() >= c.Campaign.Probability {
			continue
		}
		u, e := url.Parse(plan[i].Path)
		if e != nil {
			return e
		}
		q := u.Query()
		for _, k := range keys {
			if q.Has(k) {
				continue
			}
			v := fmt.Sprintf("bench_%016x_%d", rng.Uint64(), i)
			switch k {
			case "utm_source":
				v = "google"
			case "utm_medium":
				v = "cpc"
			case "gad_source":
				v = "1"
			case "gad_campaignid":
				v = fmt.Sprint(rng.Int63())
			}
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		plan[i].Path = u.String()
	}
	return nil
}

func campaignPlan(plan []WorkItem, c Config, x Case) error {
	if x.CampaignEnabled != nil {
		c.Campaign.Enabled = *x.CampaignEnabled
	}
	return addCampaigns(plan, c)
}
